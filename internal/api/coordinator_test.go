package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/forge"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// seedPlatformRepo is a bare repo holding files on main.
func seedPlatformRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	bare, work := filepath.Join(dir, "platform.git"), filepath.Join(dir, "work")
	run := func(wd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run(dir, "init", "-q", "--bare", "-b", "main", bare)
	run(dir, "init", "-q", "-b", "main", work)
	for path, content := range files {
		full := filepath.Join(work, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(work, "add", "-A")
	run(work, "commit", "-qm", "seed")
	run(work, "remote", "add", "origin", bare)
	run(work, "push", "-q", "origin", "main")
	return bare
}

// newCoordinatorRig is a grant rig whose server stands over a real
// coordinator, the server its authorizer, the platform repo seeded with a
// committed DRS configuration, and project grants made to a sync account.
// The forge has no repo and creates any asked for.
func newCoordinatorRig(t *testing.T) *gateRig {
	g := newGrantCluster(t)
	forgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("{}"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(forgeSrv.Close)
	platform := seedPlatformRepo(t, map[string]string{
		"descheduler/kubedescheduler.yaml": "apiVersion: operator.openshift.io/v1\nkind: KubeDescheduler\nmetadata:\n  name: cluster\n  namespace: " + descheduler + "\nspec:\n  mode: Automatic\n",
	})
	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	c := changeset.New(store, repos, forge.NewFactory(forgeSrv.URL, forge.StaticToken("tok"), false, ""), nil, nil, nil, "main", "dotvirt/proposed").
		WithSyncNamespace("dotvirt-sync")
	// The release strips label residue under the caller's token.
	g.fc.on(http.MethodPatch, "/api/v1/namespaces/orphans-ns", func(w http.ResponseWriter, r *http.Request) {
		if !g.fc.ssar(bearer(r), &authzv1.ResourceAttributes{Verb: verbPatch, Resource: "namespaces", Namespace: "orphans-ns", Name: "orphans-ns"}) {
			writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
			return
		}
		writeObj(w, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "orphans-ns"}})
	})
	g.s = g.fc.server(c, Config{PlatformRepo: platform, BaseBranch: "main"})
	c.UseAuthorizer(g.s)
	return g
}

// Every tenancy and DRS route, run through the real coordinator with the
// server as its authorizer, is allowed exactly when its capability field
// says so: with every right the change may need, without each one in turn,
// and with none. The rights only generate the grant sets; the verdicts on
// both sides are the gate's own.
func TestCapabilitiesEqualTheCoordinatorsGate(t *testing.T) {
	g := newCoordinatorRig(t)
	rbac := "rbac.authorization.k8s.io"
	full := []grant{
		{verb: verbGet, resource: "namespaces"},
		{verb: verbCreate, resource: "namespaces"},
		{verb: verbUpdate, resource: "namespaces"},
		{verb: verbPatch, resource: "namespaces"},
		{verb: verbCreate, group: rbac, resource: "rolebindings"},
		{verb: verbUpdate, group: rbac, resource: "rolebindings"},
		{verb: verbBind, group: rbac, resource: "clusterroles", name: "admin"},
		{verb: verbBind, group: rbac, resource: "clusterroles", name: tenancy.TenantEgressRole},
		kind(verbCreate, model.KindUDN, ""),
		{verb: verbCreate, group: "operator.openshift.io", resource: "kubedeschedulers", ns: descheduler},
		{verb: verbUpdate, group: "operator.openshift.io", resource: "kubedeschedulers", ns: descheduler},
		{verb: verbDelete, group: "operator.openshift.io", resource: "kubedeschedulers", ns: descheduler},
		{verb: verbCreate, group: "operators.coreos.com", resource: "operatorgroups", ns: descheduler},
		{verb: verbCreate, group: "operators.coreos.com", resource: "subscriptions", ns: descheduler},
		kind(verbCreate, model.KindMachCfg, ""),
	}
	cl := func(f func(model.ClusterCaps) bool) func(model.ClusterCaps, model.NamespaceCaps) bool {
		return func(c model.ClusterCaps, _ model.NamespaceCaps) bool { return f(c) }
	}
	pr := func(f func(model.ProjectCaps) bool) func(model.ClusterCaps, model.NamespaceCaps) bool {
		return func(_ model.ClusterCaps, n model.NamespaceCaps) bool { return f(n.Project) }
	}
	for _, c := range []struct {
		name, ns           string
		method, path, body string
		cap                func(model.ClusterCaps, model.NamespaceCaps) bool
	}{
		{"new project", "", http.MethodPost, "/api/projects", `{"name":"team-c"}`,
			cl(func(c model.ClusterCaps) bool { return c.NamespaceCreate })},
		{"new project with a VM Network", "", http.MethodPost, "/api/projects", `{"name":"team-c","vmNetwork":{"name":"v","subnet":"10.0.0.0/24"}}`,
			cl(func(c model.ClusterCaps) bool { return c.VMNetwork })},
		{"new project with owners", "", http.MethodPost, "/api/projects", `{"name":"team-c","owners":["u"]}`,
			cl(func(c model.ClusterCaps) bool { return c.GrantAdmin })},
		{"new namespace", "team-a-dev", http.MethodPost, "/api/namespaces", `{"project":"team-a","name":"newns"}`,
			pr(func(p model.ProjectCaps) bool { return p.NewNamespace })},
		{"attach repo", "orphans-ns", http.MethodPost, "/api/projects/orphans/adopt", ``,
			pr(func(p model.ProjectCaps) bool { return p.Adopt })},
		{"attach repo with owners", "orphans-ns", http.MethodPost, "/api/projects/orphans/adopt", `{"owners":["u"]}`,
			pr(func(p model.ProjectCaps) bool { return p.GrantAdmin })},
		{"release", "orphans-ns", http.MethodPost, "/api/projects/orphans/release", ``,
			pr(func(p model.ProjectCaps) bool { return p.Release })},
		// Reconfiguring: the committed KubeDescheduler is updated, the rest of
		// the set created.
		{"drs enable", "", http.MethodPost, "/api/drs", `{"mode":"Predictive"}`,
			cl(func(c model.ClusterCaps) bool { return c.DRSEnable })},
		{"drs enable with PSI", "", http.MethodPost, "/api/drs", `{"mode":"Predictive","installPSI":true}`,
			cl(func(c model.ClusterCaps) bool { return c.DRSPSI })},
		{"drs disable", "", http.MethodDelete, "/api/drs", ``,
			cl(func(c model.ClusterCaps) bool { return c.DRSDisable })},
	} {
		t.Run(c.name, func(t *testing.T) {
			g.t = t
			ns := c.ns
			if ns == "" {
				ns = "team-a-dev"
			}
			sets := [][]grant{full, nil}
			for i := range full {
				sets = append(sets, without(full, i))
			}
			allowed := 0
			for _, gs := range sets {
				tok := g.token(gs)
				cl, nc := g.caps(tok, ns)
				want := c.cap(cl, nc)
				rec := do(g.s, tok, c.method, c.path, c.body)
				switch {
				case want && rec.Code != http.StatusOK:
					t.Errorf("capability true but route = %d %q (grants %v)", rec.Code, rec.Body.String(), gs)
				case !want && rec.Code != http.StatusForbidden:
					t.Errorf("capability false but route = %d %q (grants %v)", rec.Code, rec.Body.String(), gs)
				}
				if want {
					allowed++
				}
			}
			if allowed == 0 || allowed == len(sets) {
				t.Errorf("allowed under %d of %d grant sets: the case weighs nothing", allowed, len(sets))
			}
		})
	}
}

// liveOff is a ready live source holding no VM: drift reads git alone.
type liveOff struct{}

func (liveOff) VMManifests([]string) []model.File { return nil }
func (liveOff) Ready() bool                       { return true }

type resyncOK struct{}

func (resyncOK) Resync(context.Context, string, string, string) (model.ResyncResult, error) {
	return model.ResyncResult{Application: "app"}, nil
}

// The VM detail's resync verdict is the resync route's own, VM by VM: the
// update of a running VM, the create of one the snapshot knows absent, and a
// delete and a create under a replacing sync option - each through the real
// coordinator, under every grant set minus one, all, and none.
func TestCanResyncEqualsTheResyncGate(t *testing.T) {
	g := newGrantCluster(t)
	vm := func(name, annotations string) string {
		return "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: " + name + "\n  namespace: team-a-dev\n" + annotations + "spec:\n  runStrategy: Always\n"
	}
	tenant := seedPlatformRepo(t, map[string]string{
		"team-a-dev/web.yaml": vm("web", ""),
		"team-a-dev/db.yaml":  vm("db", ""),
		"team-a-dev/rep.yaml": vm("rep", "  annotations:\n    argocd.argoproj.io/sync-options: Replace=true\n"),
	})
	g.fc.namespaces[0].Annotations["dotvirt.io/repo"] = "file://" + tenant
	g.fc.vms = []kubevirtcorev1.VirtualMachine{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a-dev", Name: "web"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a-dev", Name: "rep"}},
	}
	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	c := changeset.New(store, repos, nil, resyncOK{}, liveOff{}, nil, "main", "dotvirt/proposed")
	g.s = g.fc.server(c, Config{BaseBranch: "main"})
	g.s.drift = argo.NewSnapshot(nil, nil)
	c.UseAuthorizer(g.s)

	full := []grant{kind(verbCreate, model.KindVM, "team-a-dev"), kind(verbUpdate, model.KindVM, "team-a-dev"), kind(verbDelete, model.KindVM, "team-a-dev")}
	sets := [][]grant{full, nil}
	for i := range full {
		sets = append(sets, without(full, i))
	}
	for _, name := range []string{"web", "db", "rep"} {
		allowed := 0
		for _, gs := range sets {
			tok := g.token(gs)
			rec := do(g.s, tok, http.MethodGet, "/api/vms/team-a-dev/"+name+"/drift", "")
			wantStatus(t, rec, http.StatusOK)
			can := decodeBody[model.DriftResult](t, rec).CanResync
			rec = do(g.s, tok, http.MethodPost, "/api/vms/team-a-dev/"+name+"/resync", "")
			switch {
			case can && rec.Code != http.StatusOK:
				t.Errorf("%s: canResync but resync = %d %q (grants %v)", name, rec.Code, rec.Body.String(), gs)
			case !can && rec.Code != http.StatusForbidden:
				t.Errorf("%s: no canResync but resync = %d %q (grants %v)", name, rec.Code, rec.Body.String(), gs)
			}
			if can {
				allowed++
			}
		}
		if allowed == 0 || allowed == len(sets) {
			t.Errorf("%s: allowed under %d of %d grant sets: the case weighs nothing", name, allowed, len(sets))
		}
	}
}

// A right on a Namespace object granted inside that namespace counts, as the
// apiserver asks it there: a project admin bound only in orphans-ns may claim
// it, and the capability agrees.
func TestNamespaceRightsInsideTheNamespaceCount(t *testing.T) {
	g := newCoordinatorRig(t)
	rbac := "rbac.authorization.k8s.io"
	in := func(verb, group, resource, name string) grant {
		return grant{verb: verb, group: group, resource: resource, ns: "orphans-ns", name: name}
	}
	tok := g.token([]grant{
		in(verbGet, "", "namespaces", "orphans-ns"),
		in(verbUpdate, "", "namespaces", "orphans-ns"),
		in(verbCreate, rbac, "rolebindings", ""),
		in(verbBind, rbac, "clusterroles", "admin"),
		in(verbBind, rbac, "clusterroles", tenancy.TenantEgressRole),
	})
	if _, nc := g.caps(tok, "orphans-ns"); !nc.Project.Adopt {
		t.Errorf("adopt capability false with the rights granted inside the namespace")
	}
	if rec := do(g.s, tok, http.MethodPost, "/api/projects/orphans/adopt", ""); rec.Code != http.StatusOK {
		t.Errorf("adopt = %d %q", rec.Code, rec.Body.String())
	}
}

// A visible project with a namespace the caller cannot see is refused by the
// tenancy routes without naming that namespace.
func TestTenancyRefusesAProjectWithAHiddenNamespace(t *testing.T) {
	g := newCoordinatorRig(t)
	g.fc.namespaces = append(g.fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "orphans-zsecret", Labels: map[string]string{"dotvirt.io/project": "orphans"}}})
	c := g.s.draft.(*changeset.Coordinator)
	g.s = g.fc.server(c, g.s.cfg)
	c.UseAuthorizer(g.s)
	tok := g.token([]grant{
		{verb: verbGet, resource: "namespaces", name: "orphans-ns"},
		{verb: verbUpdate, resource: "namespaces", name: "orphans-ns"},
		{verb: verbPatch, resource: "namespaces", name: "orphans-ns"},
	})
	for _, path := range []string{"/api/projects/orphans/adopt", "/api/projects/orphans/release"} {
		rec := do(g.s, tok, http.MethodPost, path, "")
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "zsecret") {
			t.Errorf("%s = %d %q, want an unnamed 403", path, rec.Code, rec.Body.String())
		}
	}
}
