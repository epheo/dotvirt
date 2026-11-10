package api

import (
	"context"
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
	c := changeset.New(g.store, repos, nil, nil, nil, strayIn(g.repo["team-a"]), "main", "dotvirt/proposed")
	g.s = fc.server(c, Config{PlatformRepo: g.repo[platformProjectName], BaseBranch: "main"})
	c.UseAuthorizer(g.s)
	return g
}

// strayIn is ArgoCD about to prune one VM in each namespace of repo.
type strayIn string

func (r strayIn) PrunePending(repo string, namespaces []string) []model.ObjectRef {
	if repo != string(r) {
		return nil
	}
	var out []model.ObjectRef
	for _, ns := range namespaces {
		out = append(out, model.ObjectRef{Kind: "VirtualMachine", Namespace: ns, Name: "stray"})
	}
	return out
}

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

// A PR the user opened in the forge on the head dotvirt pushed takes the
// changes off their draft once the lane lists it; one on another head does
// not.
func TestDraftsAdoptThePROpenedOnThePushedHead(t *testing.T) {
	g := newDraftsRig(t, false)
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
	lane := func(head string) {
		g.s.proposals.Put("team-a", []model.Proposal{{Project: "team-a", PRNumber: 9, Branch: branch, Head: head}})
	}

	// team-a's prune warning keeps it listed: the count is what moves.
	staged := func() int {
		for _, d := range g.drafts(t, "tenant").Drafts {
			if d.Project == "team-a" {
				return d.Draft.Count
			}
		}
		return 0
	}
	lane("moved")
	if n := staged(); n != 1 {
		t.Fatalf("a PR on another head adopted the draft: %d staged", n)
	}
	lane("pushed")
	if n := staged(); n != 0 {
		t.Fatalf("the PR on the pushed head left %d staged", n)
	}
	if rec, _, _ := g.store.Proposal("tenant", "team-a"); rec.PR != 9 {
		t.Errorf("record PR = %d, want 9", rec.PR)
	}
}
