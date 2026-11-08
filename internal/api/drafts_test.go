package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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
	wantStatus(t, g.do("holder", http.MethodGet, "/api/draft?project=platform"), http.StatusServiceUnavailable)
	wantStatus(t, g.do("holder", http.MethodDelete, "/api/draft?project=platform"), http.StatusNoContent)
	wantStatus(t, g.do("holder", http.MethodGet, "/api/draft?project=platform"), http.StatusForbidden)
}

// drafts reads user's drafts in one call.
func (g *draftsRig) drafts(t *testing.T, user string) model.DraftsView {
	t.Helper()
	rec := g.do(user, http.MethodGet, "/api/drafts")
	wantStatus(t, rec, http.StatusOK)
	return decodeBody[model.DraftsView](t, rec)
}

// union is what the per-project draft route answers user, project by
// project, kept as the store kept it: a draft holding an entry or carrying a
// warning, a failure, and nothing for a refusal or an unknown project.
func (g *draftsRig) union(t *testing.T, user string) model.DraftsView {
	t.Helper()
	out := model.DraftsView{Drafts: []model.ProjectDraft{}, Failed: []model.ProjectError{}}
	for _, p := range []string{"broken", "team-a", "team-b", platformProjectName} {
		rec := g.do(user, http.MethodGet, "/api/draft?project="+p)
		switch rec.Code {
		case http.StatusOK:
			if v := decodeBody[model.DraftView](t, rec); v.Count > 0 || v.Warning != "" {
				out.Drafts = append(out.Drafts, model.ProjectDraft{Project: p, Draft: v})
			}
		case http.StatusForbidden, http.StatusNotFound:
		default:
			out.Failed = append(out.Failed, model.ProjectError{Project: p, Error: strings.TrimSpace(rec.Body.String())})
		}
	}
	return out
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
		if want := g.union(t, c.user); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: one call = %+v\nwant the draft route's union %+v", c.user, got, want)
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
	wantStatus(t, g.do("reader", http.MethodGet, "/api/draft?project=platform"), http.StatusOK)
	if calls := g.fc.seen(); len(calls) > 0 {
		t.Errorf("drafts read the cluster beyond its reviews: %q", calls)
	}
}
