package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/netstate"
	"github.com/epheo/dotvirt/internal/publish"
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
// The forge has no repo and creates any asked for: team-a's is one it lost.
// newCoordinatorRig is the coordinator over a platform repo holding the
// DRS configuration, team-a-dev's admin grant, and extra.
func newCoordinatorRig(t *testing.T, extra ...map[string]string) *gateRig {
	return coordinatorRigWith(t, nil, extra...)
}

// coordinatorRigWith is newCoordinatorRig with setup run on the cluster
// before the server starts.
func coordinatorRigWith(t *testing.T, setup func(*gateRig), extra ...map[string]string) *gateRig {
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
	g.fc.namespaces[0].Annotations["dotvirt.io/repo"] = forgeSrv.URL + "/o/team-a.git"
	g.fc.namespaces = append(g.fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a-publish", Labels: map[string]string{tenancy.PublishLabel: "team-a"}}})
	if setup != nil {
		setup(g)
	}
	files := map[string]string{
		"descheduler/kubedescheduler.yaml": "apiVersion: operator.openshift.io/v1\nkind: KubeDescheduler\nmetadata:\n  name: cluster\n  namespace: " + descheduler + "\nspec:\n  mode: Automatic\n",
		// team-a-dev's admin grant: publishing grants route writes to its subjects.
		"rbac/team-a-dev.yaml": "apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: team-a-dev-admins\n  namespace: team-a-dev\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: admin\nsubjects:\n- apiGroup: rbac.authorization.k8s.io\n  kind: User\n  name: alice\n",
	}
	for _, e := range extra {
		maps.Copy(files, e)
	}
	platform := seedPlatformRepo(t, files)
	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	publishing := publish.Settings{Domain: "apps.example.com", GatewayClass: "portail", Issuer: "letsencrypt", ProjectLabel: "dotvirt.io/project", PortailNamespace: "portail-system"}
	c := changeset.New(store, repos, forge.NewFactory(forgeSrv.URL, forge.StaticToken("tok"), false, ""), nil, nil, nil, "main", "dotvirt/proposed").
		WithSyncNamespace("dotvirt-sync").WithPublishing(publishing)
	// The release strips label residue under the caller's token.
	g.fc.on(http.MethodPatch, "/api/v1/namespaces/orphans-ns", func(w http.ResponseWriter, r *http.Request) {
		if !g.fc.ssar(bearer(r), &authzv1.ResourceAttributes{Verb: verbPatch, Resource: "namespaces", Namespace: "orphans-ns", Name: "orphans-ns"}) {
			writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
			return
		}
		writeObj(w, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "orphans-ns"}})
	})
	g.s = g.fc.server(c, Config{PlatformRepo: platform, BaseBranch: "main", Publish: publishing})
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
		{verb: verbBind, group: rbac, resource: "clusterroles", name: tenancy.TenantRoutesRole},
		{verb: verbBind, group: rbac, resource: "clusterroles", name: tenancy.TenantGatewayViewRole},
		kind(verbCreate, model.KindUDN, ""),
		kind(verbCreate, model.KindCUDN, ""),
		kind(verbCreate, model.KindGateway, ""),
		kind(verbCreate, model.KindCert, ""),
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
		{"new project with its VM Network", "", http.MethodPost, "/api/projects", `{"name":"team-c","projectNetwork":{"name":"team-c-net","subnet":"10.0.0.0/24"}}`,
			cl(func(c model.ClusterCaps) bool { return c.ProjectNetwork })},
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
		{"recover repo", "team-a-dev", http.MethodPost, "/api/projects/team-a/adopt", ``,
			pr(func(p model.ProjectCaps) bool { return p.Adopt })},
		{"sync access", "team-a-dev", http.MethodPost, "/api/projects/team-a/sync-access", ``,
			pr(func(p model.ProjectCaps) bool { return p.SyncAccess })},
		// The project's Gateway, its certificate and the route grant to
		// team-a-dev's admin-grant subjects.
		{"publishing", "team-a-dev", http.MethodPost, "/api/projects/team-a/publishing", ``,
			pr(func(p model.ProjectCaps) bool { return p.Publishing })},
		// orphans has no publish namespace: its Namespace alone.
		{"publish namespace", "orphans-ns", http.MethodPost, "/api/projects/orphans/publish-namespace", ``,
			pr(func(p model.ProjectCaps) bool { return p.PublishNamespaceCreate })},
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

// "On" is what Kubernetes shows the caller: only one who may list gateways in
// the project's publish namespace, whose publishing git holds, is told it is
// on, whatever else they may do.
func TestPublishingOnFollowsGatewayReads(t *testing.T) {
	files, err := publish.Manifests(publish.Settings{Domain: "apps.example.com", GatewayClass: "portail", Issuer: "letsencrypt", ProjectLabel: "dotvirt.io/project"}, "team-a", "team-a-publish", false)
	if err != nil {
		t.Fatal(err)
	}
	alice := []netgen.Subject{{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "alice"}}
	_, routes, err := netgen.RoutesManifest(netgen.RoleBindingSpec{
		Namespace: "team-a-dev", Project: "team-a", Kept: alice,
		SyncAccount: netgen.ServiceAccountSubject("dotvirt-sync", "dotvirt-p-team-a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	viewPath, view, err := netgen.GatewayViewManifest(netgen.RoleBindingSpec{Namespace: "team-a-publish", Kept: alice})
	if err != nil {
		t.Fatal(err)
	}
	published := map[string]string{"rbac/team-a-dev/team-a-dev-routes.yaml": string(routes), viewPath: string(view)}
	for _, f := range files {
		published[f.Path] = string(f.Content)
	}
	g := newCoordinatorRig(t, published)
	if _, nc := g.caps(g.token([]grant{kind(verbList, model.KindGateway, "team-a-dev")}), "team-a-dev"); nc.Project.PublishingOn {
		t.Error("on, told to a caller who may list gateways in the project's namespace alone")
	}
	_, nc := g.caps(g.token([]grant{kind(verbList, model.KindGateway, "team-a-publish")}), "team-a-dev")
	if !nc.Project.PublishingOn || nc.Project.Publishing || nc.Project.PublishNamespace != "team-a-publish" {
		t.Errorf("publishingOn %v, publishing %v, in %q: want on in team-a-publish, nothing to stage", nc.Project.PublishingOn, nc.Project.Publishing, nc.Project.PublishNamespace)
	}
}

// A project the cluster holds no publish namespace for is told so, naming it,
// and only to a caller who could otherwise stage its publishing or the
// namespace; the route refuses it and stages nothing.
func TestPublishingWaitsOnThePublishNamespace(t *testing.T) {
	g := newCoordinatorRig(t)
	able := g.token([]grant{
		{verb: verbGet, resource: "namespaces"},
		{verb: verbPatch, resource: "namespaces"},
		kind(verbCreate, model.KindGateway, ""),
		kind(verbCreate, model.KindCert, ""),
	})
	_, nc := g.caps(able, "orphans-ns")
	if !nc.Project.PublishingUnhoused || nc.Project.Publishing || nc.Project.PublishNamespaceCreate || nc.Project.PublishNamespace != "orphans-publish" || len(nc.Project.PublishingReach) != 0 {
		t.Errorf("caps %+v: want unhoused in orphans-publish, nothing reached, no namespace create without the right", nc.Project)
	}
	if _, nc := g.caps(g.token(nil), "orphans-ns"); nc.Project.PublishingUnhoused {
		t.Error("unhoused, told to a caller who could stage nothing")
	}
	if rec := do(g.s, able, http.MethodPost, "/api/projects/orphans/publishing", ""); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "orphans-publish") {
		t.Errorf("publishing = %d %q, want 400 naming orphans-publish", rec.Code, rec.Body.String())
	}
	creator := g.token([]grant{{verb: verbGet, resource: "namespaces"}, {verb: verbCreate, resource: "namespaces"}})
	if _, nc := g.caps(creator, "orphans-ns"); !nc.Project.PublishNamespaceCreate || !nc.Project.PublishingUnhoused || nc.Project.PublishNamespace != "orphans-publish" {
		t.Errorf("caps %+v: want the publish namespace offered to one who may create it", nc.Project)
	}
	rec := do(g.s, creator, http.MethodPost, "/api/projects/orphans/publish-namespace", "")
	var view model.DraftView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &view) != nil || len(view.Items) != 1 || view.Items[0].Name != "orphans-publish" || !strings.Contains(view.Items[0].YAML, tenancy.PublishLabel+": orphans") {
		t.Errorf("publish namespace = %d %q, want orphans-publish alone staged", rec.Code, rec.Body.String())
	}
}

// A publish namespace's name held by a user's own project is told as such to
// one who could create the namespace, and the action is refused: the name
// must go first.
func TestPublishNameHeldByAUsersProject(t *testing.T) {
	g := coordinatorRigWith(t, func(g *gateRig) {
		g.fc.namespaces = append(g.fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: "orphans-publish", Annotations: map[string]string{tenancy.RequesterAnnotation: "mallory"},
		}})
	})
	creator := g.token([]grant{{verb: verbGet, resource: "namespaces"}, {verb: verbCreate, resource: "namespaces"},
		kind(verbCreate, model.KindGateway, ""), kind(verbCreate, model.KindCert, "")})
	_, nc := g.caps(creator, "orphans-ns")
	if !nc.Project.PublishingUnhoused || !nc.Project.PublishNameHeld || !nc.Project.PublishNameRequested || nc.Project.PublishNamespaceCreate {
		t.Errorf("caps %+v: want unhoused, the name held by a user's own project, no create", nc.Project)
	}
	if rec := do(g.s, creator, http.MethodPost, "/api/projects/orphans/publish-namespace", ""); rec.Code != http.StatusConflict {
		t.Errorf("publish namespace = %d %q, want 409", rec.Code, rec.Body.String())
	}
}

// A new namespace joins its project's VM Network only where the project has
// one: labeled for a network that does not exist, it could never run a pod.
// The project says it has one, and a new project never takes over a live
// network's name.
func TestJoiningTheProjectNetwork(t *testing.T) {
	g := onVPC(t, "team-a-dev")
	tok := publisher(g)
	if _, nc := g.caps(tok, "team-a-dev"); nc.Project.ProjectNetwork {
		t.Error("team-a told it has a VM Network to join without one")
	}
	if rec := do(g.s, tok, http.MethodPost, "/api/namespaces", `{"project":"team-a","name":"newns","projectNetwork":true}`); rec.Code != http.StatusBadRequest {
		t.Errorf("joining a network team-a lacks = %d %q, want 400", rec.Code, rec.Body.String())
	}
	if rec := do(g.s, tok, http.MethodPost, "/api/projects", `{"name":"team-z","projectNetwork":{"name":"vpc","subnet":"10.9.0.0/16"}}`); rec.Code != http.StatusConflict {
		t.Errorf("a new project naming the live vpc network = %d %q, want 409", rec.Code, rec.Body.String())
	}

	h := coordinatorRigWith(t, func(g *gateRig) {
		g.fc.namespaces[0].Labels[tenancy.NetworkLabel] = "team-a"
	})
	tok = publisher(h)
	if _, nc := h.caps(tok, "team-a-dev"); !nc.Project.ProjectNetwork {
		t.Error("team-a not told it has a VM Network to join")
	}
	rec := do(h.s, tok, http.MethodPost, "/api/namespaces", `{"project":"team-a","name":"newns","projectNetwork":true}`)
	var view model.DraftView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &view) != nil {
		t.Fatalf("joining team-a's network = %d %q", rec.Code, rec.Body.String())
	}
	for _, it := range view.Items {
		if it.Name == "newns" && (!strings.Contains(it.YAML, tenancy.NetworkLabel+": team-a") || !strings.Contains(it.YAML, "k8s.ovn.org/primary-user-defined-network")) {
			t.Errorf("newns not on team-a's network:\n%s", it.YAML)
		}
	}
}

// onVPC is the coordinator rig with a primary CUDN, vpc, generated in each
// of namespaces, its netstate synced.
func onVPC(t *testing.T, namespaces ...string) *gateRig {
	t.Helper()
	g := newCoordinatorRig(t)
	resources := func(gv string, kinds ...model.Kind) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			list := metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: gv}
			for _, k := range kinds {
				list.APIResources = append(list.APIResources, metav1.APIResource{Name: k.Plural, Kind: k.Kind, Namespaced: k == model.KindUDN || k == model.KindNAD,
					Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}})
			}
			writeObj(w, list)
		}
	}
	g.fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1", resources("k8s.ovn.org/v1", model.KindCUDN, model.KindUDN))
	g.fc.on(http.MethodGet, "/apis/k8s.cni.cncf.io/v1", resources("k8s.cni.cncf.io/v1", model.KindNAD))
	g.fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1/clusteruserdefinednetworks", unstructuredList("k8s.ovn.org/v1", "ClusterUserDefinedNetwork", map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork",
		"metadata": map[string]any{"name": "vpc", "uid": "vpc", "resourceVersion": "1"},
		"spec":     map[string]any{"network": map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary", "subnets": []any{"10.40.0.0/16"}}}},
	}))
	g.fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1/userdefinednetworks", unstructuredList("k8s.ovn.org/v1", "UserDefinedNetwork"))
	var nads []map[string]any
	for _, ns := range namespaces {
		nads = append(nads, map[string]any{
			"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
			"metadata": map[string]any{
				"name": "vpc", "namespace": ns, "resourceVersion": "1",
				"ownerReferences": []any{map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork", "name": "vpc", "uid": "vpc"}},
			},
		})
	}
	g.fc.on(http.MethodGet, "/apis/k8s.cni.cncf.io/v1/network-attachment-definitions", unstructuredList("k8s.cni.cncf.io/v1", "NetworkAttachmentDefinition", nads...))
	ns := netstate.New(g.fc.sa, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns.Run(ctx)
	g.s.netstate = ns
	for deadline := time.Now().Add(10 * time.Second); ns.PrimaryNetwork(namespaces[0]) != "cudn:vpc"; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("netstate never put %s on vpc", namespaces[0])
		}
	}
	return g
}

// publisher may stage every file publishing on a VM Network stages.
func publisher(g *gateRig) string {
	rbac := "rbac.authorization.k8s.io"
	return g.token([]grant{
		{verb: verbGet, resource: "namespaces"},
		{verb: verbPatch, resource: "namespaces"},
		{verb: verbCreate, resource: "namespaces"},
		{verb: verbCreate, group: rbac, resource: "rolebindings"},
		{verb: verbUpdate, group: rbac, resource: "rolebindings"},
		{verb: verbBind, group: rbac, resource: "clusterroles"},
		{verb: verbBind, group: rbac, resource: "roles"},
		kind(verbCreate, model.KindGateway, ""),
		kind(verbCreate, model.KindCert, ""),
		kind(verbCreate, model.KindGWParams, ""),
	})
}

// A publish namespace on the project's VM Network makes a Gateway that polls
// portail through the apiserver: the network is read for the publish
// namespace itself, so until the class says Gateways are isolated publishing
// waits, and it reaches only the project namespaces on that network.
func TestPublishingOnAVMNetworkWaitsOnIsolation(t *testing.T) {
	g := onVPC(t, "team-a-publish", "team-a-dev")
	_, nc := g.caps(publisher(g), "team-a-dev")
	if !nc.Project.PublishingWaits || nc.Project.Publishing || !slices.Equal(nc.Project.PublishingReach, []string{"team-a-dev"}) {
		t.Errorf("caps %+v: want waiting on isolation, reaching team-a-dev", nc.Project)
	}
}

// A publish namespace on no network of the project's reaches nothing:
// publishing is not offered, and the project is told why rather than offered
// a stage the route refuses.
func TestPublishingApartFromEveryNamespace(t *testing.T) {
	g := onVPC(t, "team-a-dev")
	tok := publisher(g)
	_, nc := g.caps(tok, "team-a-dev")
	if !nc.Project.PublishingApart || nc.Project.Publishing || nc.Project.PublishingUnhoused || nc.Project.PublishingWaits {
		t.Errorf("caps %+v: want apart alone", nc.Project)
	}
	if rec := do(g.s, tok, http.MethodPost, "/api/projects/team-a/publishing", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("publishing = %d %q, want 400", rec.Code, rec.Body.String())
	}
}

// Granting a project's sync account lands RoleBindings alone: rights on
// those in its namespaces, and none on any Namespace, grant it, and the
// capability agrees.
func TestSyncAccessNeedsOnlyItsGrants(t *testing.T) {
	g := newCoordinatorRig(t)
	rbac := "rbac.authorization.k8s.io"
	in := func(verb, resource, name string) grant {
		return grant{verb: verb, group: rbac, resource: resource, ns: "team-a-dev", name: name}
	}
	tok := g.token([]grant{
		in(verbCreate, "rolebindings", ""),
		in(verbUpdate, "rolebindings", ""),
		in(verbBind, "clusterroles", "admin"),
		in(verbBind, "clusterroles", tenancy.TenantEgressRole),
	})
	if _, nc := g.caps(tok, "team-a-dev"); !nc.Project.SyncAccess {
		t.Errorf("sync access capability false with the grants' rights")
	}
	if rec := do(g.s, tok, http.MethodPost, "/api/projects/team-a/sync-access", ""); rec.Code != http.StatusOK {
		t.Errorf("sync access = %d %q", rec.Code, rec.Body.String())
	}
}

// A visible project with a namespace the caller cannot see is refused by the
// tenancy routes without naming that namespace, whatever rights they hold
// there, and their capabilities agree.
func TestTenancyRefusesAProjectWithAHiddenNamespace(t *testing.T) {
	g := newCoordinatorRig(t)
	g.fc.namespaces = append(g.fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "orphans-zsecret", Labels: map[string]string{"dotvirt.io/project": "orphans"}}})
	c := g.s.draft.(*changeset.Coordinator)
	g.s = g.fc.server(c, g.s.cfg)
	c.UseAuthorizer(g.s)
	rbac := "rbac.authorization.k8s.io"
	gs := []grant{
		{verb: verbCreate, group: rbac, resource: "rolebindings"},
		{verb: verbBind, group: rbac, resource: "clusterroles", name: "admin"},
		{verb: verbBind, group: rbac, resource: "clusterroles", name: tenancy.TenantEgressRole},
	}
	gs = append(gs, grant{verb: verbGet, resource: "namespaces", name: "orphans-ns"})
	for _, ns := range []string{"orphans-ns", "orphans-zsecret"} {
		gs = append(gs, grant{verb: verbUpdate, resource: "namespaces", name: ns}, grant{verb: verbPatch, resource: "namespaces", name: ns})
	}
	tok := g.token(gs)
	for _, path := range []string{"/api/projects/orphans/adopt", "/api/projects/orphans/release", "/api/projects/orphans/sync-access"} {
		rec := do(g.s, tok, http.MethodPost, path, "")
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "zsecret") {
			t.Errorf("%s = %d %q, want an unnamed 403", path, rec.Code, rec.Body.String())
		}
	}
	if _, nc := g.caps(tok, "orphans-ns"); nc.Project.Adopt || nc.Project.GrantAdmin || nc.Project.Release || nc.Project.SyncAccess {
		t.Errorf("capabilities %+v, want every tenancy-target action false", nc.Project)
	}
}
