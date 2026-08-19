package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// stagingDraft records the staging calls the transport routes into it; the
// embedded interface panics on anything a test doesn't expect to be reached.
type stagingDraft struct {
	Draft
	proposed  []model.ProposeRequest
	unstaged  []string
	discarded []string
}

func (d *stagingDraft) Propose(id auth.Identity, proj project.ProjectInfo, req model.ProposeRequest) (model.ProposeResult, error) {
	if req.Title == "" {
		return model.ProposeResult{}, fmt.Errorf("%w: title is required", model.ErrInvalid)
	}
	d.proposed = append(d.proposed, req)
	return model.ProposeResult{Branch: "dotvirt/proposed/u/" + proj.Name, Pushed: true, PRNumber: 7}, nil
}

func (d *stagingDraft) Unstage(id auth.Identity, proj project.ProjectInfo, resource, namespace, name string) error {
	d.unstaged = append(d.unstaged, proj.Name+":"+namespace+"/"+name)
	return nil
}

func (d *stagingDraft) Get(id auth.Identity, proj project.ProjectInfo) (model.DraftView, error) {
	return model.DraftView{Base: "main"}, nil
}

// draftServer is the transport under test with a fake apiserver behind it: the
// SSAR gate allows exactly admin-token, and the cluster-wide namespace list is
// empty - so tenant project resolution finds nothing (the not-found paths) while
// the platform tier resolves for the admin.
func draftServer(t *testing.T, d Draft) *Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			resp := authzv1.SelfSubjectAccessReview{
				TypeMeta: metav1.TypeMeta{Kind: "SelfSubjectAccessReview", APIVersion: "authorization.k8s.io/v1"},
				Status:   authzv1.SubjectAccessReviewStatus{Allowed: r.Header.Get("Authorization") == "Bearer admin-token"},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/api/v1/namespaces":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(corev1.NamespaceList{
				TypeMeta: metav1.TypeMeta{Kind: "NamespaceList", APIVersion: "v1"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	cfg := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: t\n  cluster: {server: %s}\ncontexts:\n- name: t\n  context: {cluster: t, user: t}\ncurrent-context: t\nusers:\n- name: t\n  user: {token: unused}\n", srv.URL)
	if err := os.WriteFile(kubeconfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := cluster.NewFactory(kubeconfig)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	bus := eventbus.New()
	sa := cluster.NewClient(fake.NewSimpleClientset(), nil, nil)
	return NewServer(Deps{
		ClusterFactory: f,
		State:          clusterstate.New(sa, "dotvirt.io/project", bus),
		Bus:            bus,
		Resolver:       project.NewResolver("dotvirt.io/project", "dotvirt.io/repo", ""),
		Draft:          d,
		Config:         Config{PlatformRepo: "https://forge/platform.git", BaseBranch: "main"},
	})
}

func asUser(r *http.Request, token string) *http.Request {
	return r.WithContext(auth.NewContext(r.Context(), auth.Identity{Token: token, Username: "u"}))
}

// The whole-draft routes carry the project in the query; forgetting it must be
// a 400 naming the parameter, never a panic or an empty-project lookup.
func TestDraftRoutesRequireProject(t *testing.T) {
	s := draftServer(t, &stagingDraft{})
	for _, m := range []struct{ method, path string }{
		{http.MethodGet, "/api/draft"},
		{http.MethodDelete, "/api/draft"},
		{http.MethodPost, "/api/draft/propose"},
	} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, asUser(httptest.NewRequest(m.method, m.path, strings.NewReader("{}")), "admin-token"))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "project") {
			t.Errorf("%s %s = %d %q, want 400 naming the project param", m.method, m.path, rec.Code, rec.Body.String())
		}
	}
}

// A VM create must peek the spec's namespace BEFORE resolving a project - the
// path carries none - and refuse a body without one (or unparseable JSON).
func TestCreateVMRefusesBadBody(t *testing.T) {
	s := draftServer(t, &stagingDraft{})
	cases := []struct {
		body string
		want string
	}{
		{`{"name":"x"}`, "namespace is required"},
		{`{not json`, "invalid"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost, "/api/vms", strings.NewReader(c.body)), "admin-token"))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("create %q = %d %q, want 400 %q", c.body, rec.Code, rec.Body.String(), c.want)
		}
	}
}

// A VM route in a namespace outside the caller's visible projects is NOT FOUND
// - the response that keeps one tenant from probing another's namespaces.
func TestVMRouteOutsideVisibleProjectsIs404(t *testing.T) {
	s := draftServer(t, &stagingDraft{})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost,
		"/api/vms/other-tenant/secret-vm/edit", strings.NewReader(`{"sourceFile":"vms/x.yaml"}`)), "admin-token"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d %q, want 404", rec.Code, rec.Body.String())
	}
}

// The platform draft: a tenant token (denied by every SSAR) must not reach it -
// the propose/unstage routes are the caller's own draft, but the platform lane
// exists only for callers holding a platform authoring signal.
func TestPlatformDraftGatedBySSAR(t *testing.T) {
	d := &stagingDraft{}
	s := draftServer(t, d)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost,
		"/api/draft/propose?project=platform", strings.NewReader(`{"title":"t","message":""}`)), "tenant-token"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tenant propose = %d %q, want 403", rec.Code, rec.Body.String())
	}
	if len(d.proposed) != 0 {
		t.Fatal("a denied propose reached the coordinator")
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodDelete,
		"/api/draft/cluster/an-uplink?project=platform&resource=uplink", nil), "tenant-token"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tenant unstage = %d %q, want 403", rec.Code, rec.Body.String())
	}
}

// The admin path end to end: propose validates its body AFTER the scope gate,
// a successful propose reaches the coordinator exactly once and nudges the
// proposals refresher so the new PR lane appears without waiting for the poll.
func TestPlatformProposeFlow(t *testing.T) {
	d := &stagingDraft{}
	s := draftServer(t, d)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost,
		"/api/draft/propose?project=platform", strings.NewReader(`{not json`)), "admin-token"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost,
		"/api/draft/propose?project=platform", strings.NewReader(`{"title":"drs: enable","message":""}`)), "admin-token"))
	if rec.Code != http.StatusOK {
		t.Fatalf("propose = %d %q, want 200", rec.Code, rec.Body.String())
	}
	var res model.ProposeResult
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil || res.PRNumber != 7 {
		t.Fatalf("propose result = %+v (%v)", res, err)
	}
	if len(d.proposed) != 1 || d.proposed[0].Title != "drs: enable" {
		t.Fatalf("coordinator saw %+v", d.proposed)
	}
	select {
	case <-s.propNudge:
	default:
		t.Fatal("a successful propose must nudge the proposals refresher")
	}
	s.propMu.Lock()
	_, tracked := s.propTargets[restfactory.TokenKey("admin-token")]
	s.propMu.Unlock()
	if !tracked {
		t.Fatal("propose must track the caller so the refresher covers the new PR")
	}
}

// The empty-title refusal must surface as the coordinator's 400, body intact -
// the UI renders this text inline in the Changes panel.
func TestProposeSurfacesCoordinatorRefusal(t *testing.T) {
	s := draftServer(t, &stagingDraft{})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost,
		"/api/draft/propose?project=platform", strings.NewReader(`{"title":"","message":""}`)), "admin-token"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "title is required") {
		t.Fatalf("empty title = %d %q, want the coordinator's own 400", rec.Code, rec.Body.String())
	}
}
