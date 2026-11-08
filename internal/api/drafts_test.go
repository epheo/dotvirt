package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
)

// draftsRig is a server over a real coordinator and its store. The tenant
// sees team-a and broken, whose repo does not exist; team-b is hidden from
// everyone. The reader lists namespaces cluster-wide, which reads the
// platform repo (it declares nothing); the holder has no right at all.
type draftsRig struct {
	s     *Server
	store *draft.Store
	repo  map[string]string // project -> repo
}

func newDraftsRig(t *testing.T, platformMissing bool) *draftsRig {
	t.Helper()
	g := &draftsRig{repo: map[string]string{}}
	seed := map[string]string{"README.md": "seed\n"}
	missing := filepath.Join(t.TempDir(), "missing.git")
	g.repo["team-a"] = seedPlatformRepo(t, seed)
	g.repo["team-b"] = seedPlatformRepo(t, seed)
	g.repo["broken"] = missing
	g.repo[platformProjectName] = seedPlatformRepo(t, seed)
	if platformMissing {
		g.repo[platformProjectName] = missing
	}
	fc := newFakeCluster(t)
	fc.namespaces = nil
	for ns, p := range map[string]string{"team-a-dev": "team-a", "team-b-dev": "team-b", "broken-ns": "broken"} {
		fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: ns, Labels: map[string]string{"dotvirt.io/project": p}, Annotations: map[string]string{"dotvirt.io/repo": g.repo[p]},
		}})
	}
	fc.readsVMs = func(token, ns string) bool { return token == "tenant" && (ns == "team-a-dev" || ns == "broken-ns") }
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
	c := changeset.New(g.store, repos, nil, nil, nil, nil, "main", "dotvirt/proposed")
	g.s = fc.server(c, Config{PlatformRepo: g.repo[platformProjectName], BaseBranch: "main"})
	c.UseAuthorizer(g.s)
	return g
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
