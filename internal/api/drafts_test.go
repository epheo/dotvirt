package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// draftsRig is a server over a real coordinator and its store. The tenant
// and idle see team-a, which ArgoCD would prune a VM in, and broken, whose
// repo does not exist; team-b is hidden from everyone. The reader lists
// namespaces cluster-wide, which reads the platform repo (it declares
// nothing); the holder has no right at all.
type draftsRig struct {
	s     *Server
	fc    *fakeCluster
	store *draft.Store
	repo  map[string]string // project -> repo
	stray stray
}

func newDraftsRig(t *testing.T, platformMissing bool) *draftsRig {
	t.Helper()
	g := &draftsRig{repo: map[string]string{}}
	// A file URL: a project's repo annotation must name its origin.
	seed := func() string { return "file://" + seedPlatformRepo(t, map[string]string{"README.md": "seed\n"}) }
	missing := "file://" + filepath.Join(t.TempDir(), "missing.git")
	g.repo["team-a"], g.repo["team-b"], g.repo["broken"] = seed(), seed(), missing
	g.repo[platformProjectName] = seed()
	if platformMissing {
		g.repo[platformProjectName] = missing
	}
	fc := newFakeCluster(t)
	g.fc = fc
	fc.namespaces = nil
	for ns, p := range map[string]string{"team-a-dev": "team-a", "team-b-dev": "team-b", "broken-ns": "broken"} {
		fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: ns, Labels: map[string]string{"dotvirt.io/project": p}, Annotations: map[string]string{"dotvirt.io/repo": g.repo[p]},
		}})
	}
	fc.readsVMs = func(token, ns string) bool {
		return (token == "tenant" || token == "idle") && (ns == "team-a-dev" || ns == "broken-ns")
	}
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		return token == "reader" && a.Verb == verbList && a.Resource == "namespaces" && a.Namespace == ""
	}
	var err error
	if g.store, err = draft.Open(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	g.stray = stray{g.repo["team-a"]: {{Kind: "VirtualMachine", Namespace: "team-a-dev", Name: "stray"}}}
	c := changeset.New(g.store, repos, nil, nil, nil, g.stray, "main", "dotvirt/proposed")
	g.s = fc.server(c, Config{PlatformRepo: g.repo[platformProjectName], BaseBranch: "main"})
	c.UseAuthorizer(g.s)
	return g
}

// stray is what ArgoCD is about to prune, per repo.
type stray map[string][]model.ObjectRef

func (s stray) PrunePending(repo string, _ ...string) ([]model.ObjectRef, bool) { return s[repo], true }

// stage puts one removal into user's draft of project, as any route would.
func (g *draftsRig) stage(t *testing.T, user, project string) {
	t.Helper()
	if err := g.store.Stage(user, project, draft.Entry{Kind: draft.KindDelete, Namespace: project + "-ns", Name: "web", SourceFile: project + "-ns/web.yaml"}); err != nil {
		t.Fatal(err)
	}
}

// do runs one request as user, whose token is their name.
func (g *draftsRig) do(user, method, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r = r.WithContext(auth.NewContext(r.Context(), auth.Identity{Token: user, Username: user}))
	rec := httptest.NewRecorder()
	g.s.Handler().ServeHTTP(rec, r)
	return rec
}

// A holder without platform read reaches their platform draft while its repo
// is unreachable: the outage is answered, never a refusal, and the draft can
// still be discarded.
func TestPlatformHolderReachesTheirDraftWhileTheRepoIsDown(t *testing.T) {
	g := newDraftsRig(t, true)
	g.stage(t, "holder", platformProjectName)
	if _, failed := projectsOf(g.drafts(t, "holder")); len(failed) != 1 || !strings.HasPrefix(failed[0], platformProjectName+": ") {
		t.Errorf("holder while the repo is down: failed %q, want the platform lane answered as an outage", failed)
	}
	wantStatus(t, g.do("holder", http.MethodDelete, "/api/draft?project=platform"), http.StatusNoContent)
	if drafts, failed := projectsOf(g.drafts(t, "holder")); len(drafts)+len(failed) != 0 {
		t.Errorf("holder after discard: drafts %q, failed %q; want no platform lane", drafts, failed)
	}
}

// drafts reads user's drafts in one call.
func (g *draftsRig) drafts(t *testing.T, user string) model.DraftsView {
	t.Helper()
	rec := g.do(user, http.MethodGet, "/api/drafts")
	wantStatus(t, rec, http.StatusOK)
	return decodeBody[model.DraftsView](t, rec)
}

func projectsOf(v model.DraftsView) (drafts, failed []string) {
	for _, d := range v.Drafts {
		drafts = append(drafts, d.Project)
	}
	for _, f := range v.Failed {
		failed = append(failed, f.Project+": "+f.Error)
	}
	return drafts, failed
}

// One call answers each draft of the caller's the draft route would, and
// nothing it would refuse: a tenant's visible projects only, never the
// stranded draft of one hidden from them; a broken repo's draft as a failure
// worded for the caller; a lane with nothing staged only when it warns; the
// platform draft for its holder without platform read, and not for a reader
// holding none.
func TestDraftsAnswerEveryDraftOfTheCaller(t *testing.T) {
	g := newDraftsRig(t, false)
	for _, p := range []string{"team-a", "team-b", "broken"} {
		g.stage(t, "tenant", p)
	}
	g.stage(t, "holder", platformProjectName)
	unreachable := "temporarily unavailable: project repo unreachable"
	for _, c := range []struct {
		user           string
		drafts, failed []string
	}{
		{"tenant", []string{"team-a"}, []string{"broken: " + unreachable}},
		{"idle", []string{"team-a"}, nil},
		{"holder", []string{platformProjectName}, nil},
		{"reader", nil, nil},
	} {
		got := g.drafts(t, c.user)
		drafts, failed := projectsOf(got)
		if !slices.Equal(drafts, c.drafts) || !slices.Equal(failed, c.failed) {
			t.Errorf("%s: drafts %q, failed %q; want %q, %q", c.user, drafts, failed, c.drafts, c.failed)
		}
		for _, f := range got.Failed {
			if strings.Contains(f.Error, g.repo[f.Project]) || strings.Contains(f.Error, "missing.git") {
				t.Errorf("%s: %s failure names its repo: %q", c.user, f.Project, f.Error)
			}
		}
	}
	if d := g.drafts(t, "idle").Drafts; len(d) == 1 && (d[0].Draft.Count != 0 || d[0].Draft.Warning == "") {
		t.Errorf("idle team-a = %+v, want the prune warning alone", d[0].Draft)
	}
	if calls := g.fc.seen(); len(calls) > 0 {
		t.Errorf("drafts read the cluster beyond its reviews: %q", calls)
	}
}

// After a platform-repo loss, the platform app holds every object it tracks
// and any platform merge prunes them all. Every caller reaching the platform
// lane is warned of all of them, each named only to a caller who may list it
// where it lives: the reader lists namespaces, not RoleBindings, and the
// holder lists neither. A repo-wide read verdict would answer from what git
// declares, which is exactly what these objects no longer are.
func TestPlatformPruneWarnsEveryCallerOfTheWholeRepo(t *testing.T) {
	g := newDraftsRig(t, false)
	g.stray[g.repo[platformProjectName]] = []model.ObjectRef{
		{Kind: "Namespace", Name: "team-b-dev"},
		{Kind: "RoleBinding", Namespace: "team-b-dev", Name: "admins"},
	}
	g.stage(t, "holder", platformProjectName)
	lane := func(user string) model.DraftView {
		t.Helper()
		for _, d := range g.drafts(t, user).Drafts {
			if d.Project == platformProjectName {
				return d.Draft
			}
		}
		t.Fatalf("%s: no platform lane", user)
		return model.DraftView{}
	}
	if v := lane("reader"); len(v.Prune) != 1 || v.Prune[0].Kind != "Namespace" || v.PruneHidden != 1 || strings.Contains(v.Warning, "admins") {
		t.Errorf("reader: %+v, want the Namespace named and the RoleBinding counted", v)
	}
	if v := lane("holder"); len(v.Prune) != 0 || v.PruneHidden != 2 || strings.Contains(v.Warning, "team-b") {
		t.Errorf("holder: %+v, want 2 counted and none named", v)
	}
}

// A tenant caller is told a pending object by name only if they may list its
// kind where it lives: seeing the namespace is not enough. idle lists VMs in
// team-a-dev, not DataVolumes.
func TestTenantPruneNamesOnlyKindsTheCallerLists(t *testing.T) {
	g := newDraftsRig(t, false)
	vm := model.ObjectRef{Kind: "VirtualMachine", Namespace: "team-a-dev", Name: "stray"}
	g.stray[g.repo["team-a"]] = []model.ObjectRef{vm, {Kind: "DataVolume", Namespace: "team-a-dev", Name: "stray-disk"}}
	for _, d := range g.drafts(t, "idle").Drafts {
		if d.Project != "team-a" {
			continue
		}
		if !slices.Equal(d.Draft.Prune, []model.ObjectRef{vm}) || d.Draft.PruneHidden != 1 || strings.Contains(d.Draft.Warning, "stray-disk") {
			t.Errorf("idle team-a = %+v, want the VM named and the DataVolume counted", d.Draft)
		}
		return
	}
	t.Fatal("idle: no team-a lane")
}

// The forge's word on a PR the user opened on the head dotvirt pushed takes
// the changes off their draft, whether or not they look: opened, it becomes
// the branch's PR; merged first, the record goes. A PR on another head, or
// into another base, settles nothing.
func TestTheForgeWebhookSettlesTheDraftItsPRCarries(t *testing.T) {
	for _, merged := range []bool{false, true} {
		g := newDraftsRig(t, false)
		g.s.cfg.WebhookSecret = "hooksecret"
		g.stage(t, "tenant", "team-a")
		entries, err := g.store.List("tenant", "team-a")
		if err != nil || len(entries) != 1 {
			t.Fatalf("staged: %v %+v", err, entries)
		}
		branch := g.s.reader.ProposedBranch(auth.Identity{Username: "tenant"}, project.ProjectInfo{Name: "team-a"})
		if err := g.store.SetProposal("tenant", "team-a", draft.Proposal{
			Repo: forge.NormalizeRepoURL(g.repo["team-a"]), Branch: branch, Head: "pushed", Entries: entries,
		}); err != nil {
			t.Fatal(err)
		}
		event := func(head, base string) {
			t.Helper()
			body := []byte(fmt.Sprintf(`{"action":"opened","pull_request":{"number":9,"state":%q,"merged":%v,`+
				`"head":{"ref":%q,"sha":%q},"base":{"ref":%q}},"repository":{"clone_url":%q}}`,
				map[bool]string{false: "open", true: "closed"}[merged], merged, branch, head, base, g.repo["team-a"]))
			if w := deliver(t, g.s, body, sign(body, "hooksecret")); w.Code != http.StatusNoContent {
				t.Fatalf("delivery: %d", w.Code)
			}
		}
		staged := func() int {
			n, _ := g.store.List("tenant", "team-a")
			return len(n)
		}
		event("moved", "main")
		event("pushed", "release")
		if n := staged(); n != 1 {
			t.Fatalf("merged=%v: a PR on another head or base settled the draft: %d staged", merged, n)
		}
		event("pushed", "main")
		if n := staged(); n != 0 {
			t.Fatalf("merged=%v: the PR on the pushed head left %d staged", merged, n)
		}
		rec, ok, _ := g.store.Proposal("tenant", "team-a")
		if merged && ok || !merged && rec.PR != 9 {
			t.Errorf("merged=%v: record %+v (present %v), want PR 9 while open and none once merged", merged, rec, ok)
		}
	}
}
