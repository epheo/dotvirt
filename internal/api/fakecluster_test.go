package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// fakeCluster is one fake apiserver behind both dotvirt's SA and every caller
// token, so a test drives a handler end to end: the SA snapshot syncs from its
// lists, and the caller's reads and writes land on its routes. admin-token
// passes every SSAR; any other token sees only what ssrr grants it.
type fakeCluster struct {
	t   *testing.T
	srv *httptest.Server

	namespaces []corev1.Namespace
	vms        []kubevirtcorev1.VirtualMachine
	vmis       []kubevirtcorev1.VirtualMachineInstance

	mu     sync.Mutex
	routes map[string]http.HandlerFunc // "METHOD /path"
	calls  []string                    // "METHOD /path" of every non-authz, non-snapshot request
	// ssar decides a SelfSubjectAccessReview; nil allows exactly admin-token.
	ssar func(token string, a *authzv1.ResourceAttributes) bool
	// ssrr answers a SelfSubjectRulesReview; nil grants nothing.
	ssrr func(token, namespace string) []authzv1.ResourceRule
}

const (
	adminToken  = "admin-token"
	tenantToken = "tenant-token"
	repoTeamA   = "https://forge/team-a.git"
)

// newFakeCluster seeds team-a (one repo-backed namespace, team-a-dev) and
// orphans (a repoless one, orphans-ns).
func newFakeCluster(t *testing.T) *fakeCluster {
	t.Helper()
	fc := &fakeCluster{t: t, routes: map[string]http.HandlerFunc{}}
	fc.namespaces = []corev1.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "team-a-dev", Labels: map[string]string{"dotvirt.io/project": "team-a"}, Annotations: map[string]string{"dotvirt.io/repo": repoTeamA}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "orphans-ns", Labels: map[string]string{"dotvirt.io/project": "orphans"}}},
	}
	return fc
}

// on registers a handler for one method and path.
func (fc *fakeCluster) on(method, path string, h http.HandlerFunc) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.routes[method+" "+path] = h
}

// seen reports the recorded calls.
func (fc *fakeCluster) seen() []string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return append([]string(nil), fc.calls...)
}

func (fc *fakeCluster) called(method, path string) bool {
	for _, c := range fc.seen() {
		if c == method+" "+path {
			return true
		}
	}
	return false
}

func writeObj(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeStatus answers a k8s Status error, so client-go maps it to an apierror.
func writeStatus(w http.ResponseWriter, code int, reason metav1.StatusReason) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure, Code: int32(code), Reason: reason, Message: string(reason),
	})
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (fc *fakeCluster) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("watch") == "true" {
		// An idle watch: the snapshot keeps what its LIST returned.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	switch r.URL.Path {
	case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
		body, _ := io.ReadAll(r.Body)
		obj, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, &authzv1.SelfSubjectAccessReview{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		attrs := obj.(*authzv1.SelfSubjectAccessReview).Spec.ResourceAttributes
		allow := bearer(r) == adminToken
		if fc.ssar != nil {
			allow = fc.ssar(bearer(r), attrs)
		}
		writeObj(w, authzv1.SelfSubjectAccessReview{
			TypeMeta: metav1.TypeMeta{Kind: "SelfSubjectAccessReview", APIVersion: "authorization.k8s.io/v1"},
			Status:   authzv1.SubjectAccessReviewStatus{Allowed: allow},
		})
		return
	case "/apis/authorization.k8s.io/v1/selfsubjectrulesreviews":
		body, _ := io.ReadAll(r.Body)
		obj, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, &authzv1.SelfSubjectRulesReview{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var rules []authzv1.ResourceRule
		if fc.ssrr != nil {
			rules = fc.ssrr(bearer(r), obj.(*authzv1.SelfSubjectRulesReview).Spec.Namespace)
		}
		writeObj(w, authzv1.SelfSubjectRulesReview{
			TypeMeta: metav1.TypeMeta{Kind: "SelfSubjectRulesReview", APIVersion: "authorization.k8s.io/v1"},
			Status:   authzv1.SubjectRulesReviewStatus{ResourceRules: rules},
		})
		return
	}
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/api/v1/namespaces":
			writeObj(w, corev1.NamespaceList{TypeMeta: metav1.TypeMeta{Kind: "NamespaceList", APIVersion: "v1"}, Items: fc.namespaces})
			return
		case "/apis/kubevirt.io/v1/virtualmachines":
			writeObj(w, kubevirtcorev1.VirtualMachineList{TypeMeta: metav1.TypeMeta{Kind: "VirtualMachineList", APIVersion: "kubevirt.io/v1"}, Items: fc.vms})
			return
		case "/apis/kubevirt.io/v1/virtualmachineinstances":
			writeObj(w, kubevirtcorev1.VirtualMachineInstanceList{TypeMeta: metav1.TypeMeta{Kind: "VirtualMachineInstanceList", APIVersion: "kubevirt.io/v1"}, Items: fc.vmis})
			return
		case "/apis/rbac.authorization.k8s.io/v1/rolebindings":
			writeObj(w, rbacv1.RoleBindingList{TypeMeta: metav1.TypeMeta{Kind: "RoleBindingList", APIVersion: "rbac.authorization.k8s.io/v1"}})
			return
		}
	}
	key := r.Method + " " + r.URL.Path
	fc.mu.Lock()
	fc.calls = append(fc.calls, key)
	h := fc.routes[key]
	fc.mu.Unlock()
	if h == nil {
		writeStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound)
		return
	}
	h(w, r)
}

// coordinator is the git plane a handler test stands up behind the transport.
type coordinator interface {
	Reader
	Draft
}

// server starts the fake apiserver, syncs the SA snapshot from it, and
// returns the API server under test over d (a planeDraft when nil).
func (fc *fakeCluster) server(d coordinator, cfg Config) *Server {
	t := fc.t
	t.Helper()
	// TLS: client-go drops a kubeconfig bearer token over plain http.
	fc.srv = httptest.NewUnstartedServer(http.HandlerFunc(fc.serve))
	fc.srv.Config.ErrorLog = log.New(io.Discard, "", 0) // handshakes cut by teardown
	fc.srv.StartTLS()
	t.Cleanup(fc.srv.Close)

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: t\n  cluster: {server: %s, insecure-skip-tls-verify: true}\ncontexts:\n- name: t\n  context: {cluster: t, user: t}\ncurrent-context: t\nusers:\n- name: t\n  user: {token: sa-token}\n", fc.srv.URL)
	if err := os.WriteFile(kubeconfig, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := cluster.NewFactory(kubeconfig)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	sa, err := f.SA()
	if err != nil {
		t.Fatalf("SA client: %v", err)
	}
	bus := eventbus.New()
	state := clusterstate.New(sa, "dotvirt.io/project", bus)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // runs before srv.Close: the idle watches end first
	state.Run(ctx)
	syncCtx, done := context.WithTimeout(ctx, 10*time.Second)
	defer done()
	if err := state.WaitForSync(syncCtx); err != nil {
		t.Fatalf("snapshot sync: %v", err)
	}
	if d == nil {
		d = &planeDraft{}
	}
	if cfg.ProjectLabel == "" {
		cfg.ProjectLabel, cfg.RepoAnnotation = "dotvirt.io/project", "dotvirt.io/repo"
	}
	return NewServer(Deps{
		ClusterFactory: f,
		State:          state,
		Bus:            bus,
		Resolver:       project.NewResolver("dotvirt.io/project", "dotvirt.io/repo", ""),
		Reader:         d,
		Draft:          d,
		Config:         cfg,
	})
}

// do runs one request through the full handler chain as token.
func do(s *Server, token, method, target, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	if token != "" {
		r = asUser(r, token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d %q, want %d", rec.Code, rec.Body.String(), want)
	}
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

// unstructuredList answers a dynamic-client LIST.
func unstructuredList(apiVersion, kind string, items ...map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if items == nil {
			items = []map[string]any{}
		}
		writeObj(w, map[string]any{"apiVersion": apiVersion, "kind": kind + "List", "metadata": map[string]any{}, "items": items})
	}
}

// echoCreate answers a create by returning the posted object, optionally
// merged with status, and records its body.
func echoCreate(got *map[string]any, status map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var obj map[string]any
		_ = json.NewDecoder(r.Body).Decode(&obj)
		if got != nil {
			*got = obj
		}
		if status != nil {
			obj["status"] = status
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(obj)
	}
}

// planeDraft records which staging call a route reached and with which
// projects; the embedded interfaces panic on anything a test does not expect.
type planeDraft struct {
	Reader
	Draft

	mu    sync.Mutex
	calls []string // "Method commitProject[,otherProject]"
	raw   []byte   // the last staged spec

	drs       model.DRSGitState
	drsErr    error
	drsDraft  model.DRSDraftState
	templates map[string][]model.Template // by library name

	releaseStaged, releaseResidue []string
}

var stagedView = model.DraftView{Base: "main", Count: 1}

func (d *planeDraft) record(call string, raw []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, call)
	d.raw = raw
}

func (d *planeDraft) only(t *testing.T, want string) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) != 1 || d.calls[0] != want {
		t.Fatalf("draft calls = %q, want [%q]", d.calls, want)
	}
	d.calls = nil
}

func (d *planeDraft) none(t *testing.T) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) != 0 {
		t.Fatalf("draft reached: %q", d.calls)
	}
}

func (d *planeDraft) DRSState(project.ProjectInfo) (model.DRSGitState, error) {
	return d.drs, d.drsErr
}

func (d *planeDraft) DRSDraft(auth.Identity, project.ProjectInfo) (model.DRSDraftState, error) {
	return d.drsDraft, nil
}

func (d *planeDraft) StageEnableDRS(_ auth.Identity, p project.ProjectInfo, spec json.RawMessage) (model.DraftView, error) {
	d.record("StageEnableDRS "+p.Name, spec)
	return stagedView, nil
}

func (d *planeDraft) StageDisableDRS(_ auth.Identity, p project.ProjectInfo) (model.DraftView, error) {
	d.record("StageDisableDRS "+p.Name, nil)
	return stagedView, nil
}

func (d *planeDraft) Templates(p project.ProjectInfo) []model.Template {
	return d.templates[p.Name]
}

func (d *planeDraft) StageDeployFromTemplate(_ auth.Identity, target, lib project.ProjectInfo, _ model.DeployTemplateRequest) (model.DraftView, error) {
	d.record("StageDeployFromTemplate "+target.Name+","+lib.Name, nil)
	return stagedView, nil
}

func (d *planeDraft) StageSaveTemplate(_ auth.Identity, commit, source project.ProjectInfo, _ model.SaveTemplateRequest) (model.DraftView, error) {
	d.record("StageSaveTemplate "+commit.Name+","+source.Name, nil)
	return stagedView, nil
}

func (d *planeDraft) StageUpdateTemplate(_ auth.Identity, commit project.ProjectInfo, _ model.UpdateTemplateRequest) (model.DraftView, error) {
	d.record("StageUpdateTemplate "+commit.Name, nil)
	return stagedView, nil
}

func (d *planeDraft) StageCreateNamespace(_ auth.Identity, commit, join project.ProjectInfo, spec json.RawMessage, _ changeset.LiveNamespaces) (model.DraftView, error) {
	d.record("StageCreateNamespace "+commit.Name+","+join.Name, spec)
	return stagedView, nil
}

func (d *planeDraft) StageCreateProject(_ auth.Identity, commit project.ProjectInfo, spec json.RawMessage, _ changeset.LiveNamespaces) (model.DraftView, error) {
	d.record("StageCreateProject "+commit.Name, spec)
	return stagedView, nil
}

func (d *planeDraft) AdoptProject(_ auth.Identity, commit, target project.ProjectInfo, owners []string, _ changeset.LiveNamespaces) (model.DraftView, error) {
	d.record("AdoptProject "+commit.Name+","+target.Name+" "+strings.Join(owners, "+"), nil)
	return stagedView, nil
}

func (d *planeDraft) ReleaseDeclared(_ auth.Identity, commit, target project.ProjectInfo) ([]string, []string, error) {
	d.record("ReleaseDeclared "+commit.Name+","+target.Name, nil)
	return d.releaseStaged, d.releaseResidue, nil
}

func (d *planeDraft) Get(auth.Identity, project.ProjectInfo) (model.DraftView, error) {
	return stagedView, nil
}
