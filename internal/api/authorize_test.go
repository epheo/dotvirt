package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"kubevirt.io/client-go/kubecli"
	kvfake "kubevirt.io/client-go/kubevirt/fake"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// fakeKubevirt serves the VM and VMI list/watch the SA snapshot runs; the
// embedded interface panics on anything else.
type fakeKubevirt struct {
	kubecli.KubevirtClient
	cs *kvfake.Clientset
}

func (f fakeKubevirt) VirtualMachine(ns string) kubecli.VirtualMachineInterface {
	return f.cs.KubevirtV1().VirtualMachines(ns)
}

func (f fakeKubevirt) VirtualMachineInstance(ns string) kubecli.VirtualMachineInstanceInterface {
	return f.cs.KubevirtV1().VirtualMachineInstances(ns)
}

// reachedDraft records which coordinator call a route reached; reaching it
// means the authoring gate let the request through.
type reachedDraft struct {
	Reader
	Draft
	mu      sync.Mutex
	reached []string
}

func (d *reachedDraft) hit(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reached = append(d.reached, name)
}

func (d *reachedDraft) take() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.reached
	d.reached = nil
	return out
}

var view = model.DraftView{Base: "main"}

func (d *reachedDraft) StageEdit(auth.Identity, project.ProjectInfo, string, string, model.EditRequest) (model.DraftView, error) {
	d.hit("StageEdit")
	return view, nil
}
func (d *reachedDraft) StageCreateVM(auth.Identity, project.ProjectInfo, json.RawMessage) (model.DraftView, error) {
	d.hit("StageCreateVM")
	return view, nil
}
func (d *reachedDraft) StageCreate(auth.Identity, project.ProjectInfo, draft.Resource, json.RawMessage) (model.DraftView, error) {
	d.hit("StageCreate")
	return view, nil
}
func (d *reachedDraft) StageDelete(auth.Identity, project.ProjectInfo, string, string, string) (model.DraftView, error) {
	d.hit("StageDelete")
	return view, nil
}
func (d *reachedDraft) StageUpdateManifest(auth.Identity, project.ProjectInfo, string, string, string, string) (model.DraftView, error) {
	d.hit("StageUpdateManifest")
	return view, nil
}
func (d *reachedDraft) StageDeployFromTemplate(auth.Identity, project.ProjectInfo, project.ProjectInfo, model.DeployTemplateRequest) (model.DraftView, error) {
	d.hit("StageDeployFromTemplate")
	return view, nil
}
func (d *reachedDraft) StageSaveTemplate(auth.Identity, project.ProjectInfo, project.ProjectInfo, model.SaveTemplateRequest) (model.DraftView, error) {
	d.hit("StageSaveTemplate")
	return view, nil
}
func (d *reachedDraft) StageUpdateTemplate(auth.Identity, project.ProjectInfo, model.UpdateTemplateRequest) (model.DraftView, error) {
	d.hit("StageUpdateTemplate")
	return view, nil
}
func (d *reachedDraft) Unstage(auth.Identity, project.ProjectInfo, string, string, string) error {
	d.hit("Unstage")
	return nil
}
func (d *reachedDraft) Discard(auth.Identity, project.ProjectInfo) error {
	d.hit("Discard")
	return nil
}
func (d *reachedDraft) Propose(auth.Identity, project.ProjectInfo, model.ProposeRequest) (model.ProposeResult, error) {
	d.hit("Propose")
	return model.ProposeResult{}, nil
}
func (d *reachedDraft) Adopt(auth.Identity, project.ProjectInfo, string, string) (model.DraftView, error) {
	d.hit("Adopt")
	return view, nil
}
func (d *reachedDraft) RestoreVersion(auth.Identity, project.ProjectInfo, string, string, string, string) (model.DraftView, error) {
	d.hit("RestoreVersion")
	return view, nil
}
func (d *reachedDraft) Revert(auth.Identity, project.ProjectInfo, string) (model.ProposeResult, error) {
	d.hit("Revert")
	return model.ProposeResult{}, nil
}

// The tokens the fake apiserver answers SSARs for: an editor may do anything,
// a viewer only read.
const (
	editorToken = "editor-token"
	viewerToken = "viewer-token"
)

func readVerb(v string) bool { return v == "get" || v == "list" || v == "watch" }

// projectOf labels every test namespace into "team", except "sneaky", which
// claims the platform tier's reserved name.
func projectOf(ns string) string {
	if ns == "sneaky" {
		return platformProjectName
	}
	return "team"
}

// tenantServer is the transport over a real SA snapshot holding the given
// project namespaces (all in project "team"), with every token seeded to see
// visible of them.
func tenantServer(t *testing.T, d *reachedDraft, nss []string, visible []string) *Server {
	t.Helper()
	f := ssarFactory(t, func(r *http.Request, ssar *authzv1.SelfSubjectAccessReview) bool {
		switch r.Header.Get("Authorization") {
		case "Bearer " + editorToken:
			return true
		case "Bearer " + viewerToken:
			return readVerb(ssar.Spec.ResourceAttributes.Verb)
		}
		return false
	})
	var objs []corev1.Namespace
	for _, ns := range nss {
		objs = append(objs, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:        ns,
			Labels:      map[string]string{"dotvirt.io/project": projectOf(ns)},
			Annotations: map[string]string{"dotvirt.io/repo": "https://forge/team.git"},
		}})
	}
	kube := fake.NewSimpleClientset(&corev1.NamespaceList{Items: objs})
	sa := cluster.NewClient(kube, fakeKubevirt{cs: kvfake.NewSimpleClientset()}, nil)
	bus := eventbus.New()
	state := clusterstate.New(sa, "dotvirt.io/project", bus)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	state.Run(ctx)
	wait, done := context.WithTimeout(ctx, 10*time.Second)
	defer done()
	if err := state.WaitForSync(wait); err != nil {
		t.Fatalf("snapshot sync: %v", err)
	}
	s := NewServer(Deps{
		ClusterFactory: f,
		State:          state,
		Bus:            bus,
		Resolver:       project.NewResolver("dotvirt.io/project", "dotvirt.io/repo", ""),
		Reader:         d,
		Draft:          d,
		Config:         Config{BaseBranch: "main"},
	})
	set := map[string]bool{}
	for _, ns := range visible {
		set[ns] = true
	}
	for _, tok := range []string{editorToken, viewerToken} {
		s.visible.Put(restfactory.TokenKey(tok), visibleSet{ns: set, ver: s.rbacVersion()})
	}
	return s
}

const hash40 = "0123456789abcdef0123456789abcdef01234567"

// authorRoutes is every tenant route that writes a project draft or opens a
// PR, with the coordinator call it reaches once the gate passes.
var authorRoutes = []struct {
	method, path, body, reaches string
}{
	{http.MethodPost, "/api/vms/web/vm1/edit", `{"sourceFile":"web/vm1.yaml"}`, "StageEdit"},
	{http.MethodPost, "/api/vms", `{"namespace":"web","name":"vm2"}`, "StageCreateVM"},
	{http.MethodPost, "/api/vms/web/vm1/delete", ``, "StageDelete"},
	{http.MethodPost, "/api/vms/web/vm1/adopt", ``, "Adopt"},
	{http.MethodPost, "/api/vms/web/vm1/restore", `{"hash":"` + hash40 + `"}`, "RestoreVersion"},
	{http.MethodPost, "/api/draft/propose?project=team", `{"title":"t"}`, "Propose"},
	{http.MethodPost, "/api/projects/team/revert", `{"hash":"` + hash40 + `"}`, "Revert"},
	{http.MethodPost, "/api/networks", `{"scope":"project","namespace":"web"}`, "StageCreate"},
	{http.MethodPost, "/api/egressfirewalls", `{"namespace":"web"}`, "StageCreate"},
	{http.MethodPost, "/api/networkpolicies", `{"namespace":"web"}`, "StageCreate"},
	{http.MethodPut, "/api/objects/networkpolicy/web/p1", `{"yaml":"kind: NetworkPolicy"}`, "StageUpdateManifest"},
	{http.MethodDelete, "/api/objects/networkpolicy/web/p1", ``, "StageDelete"},
	{http.MethodPost, "/api/objects/networkpolicy/web/p1/restore", `{"hash":"` + hash40 + `"}`, "RestoreVersion"},
	{http.MethodPost, "/api/templates/deploy", `{"namespace":"web","template":"t"}`, "StageDeployFromTemplate"},
	{http.MethodPut, "/api/templates", `{"library":"team","name":"t","yaml":"x"}`, "StageUpdateTemplate"},
	{http.MethodPost, "/api/templates", `{"sourceNamespace":"web","sourceName":"vm1"}`, "StageSaveTemplate"},
	// The adoption captures read the cluster before staging, so passing the
	// gate is all these assert for the editor.
	{http.MethodPost, "/api/namespaces/web/adopt", ``, ""},
	{http.MethodPost, "/api/objects/networkpolicy/web/p1/adopt", ``, ""},
}

func serve(s *Server, method, path, body, token string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, asUser(httptest.NewRequest(method, path, strings.NewReader(body)), token))
	return rec
}

func gateDenied(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "you may not")
}

// Read access alone must not stage, propose or revert: the viewer is refused
// by the gate before the coordinator, the editor reaches it.
func TestAuthorGateViewerForbiddenEditorPasses(t *testing.T) {
	d := &reachedDraft{}
	s := tenantServer(t, d, []string{"web"}, []string{"web"})
	for _, rt := range authorRoutes {
		name := rt.method + " " + rt.path
		t.Run(name, func(t *testing.T) {
			rec := serve(s, rt.method, rt.path, rt.body, viewerToken)
			if !gateDenied(rec) {
				t.Errorf("viewer: %d %q, want the gate's 403", rec.Code, rec.Body.String())
			}
			if got := d.take(); len(got) != 0 {
				t.Errorf("viewer reached the coordinator: %v", got)
			}
			rec = serve(s, rt.method, rt.path, rt.body, editorToken)
			if gateDenied(rec) {
				t.Errorf("editor denied by the gate: %q", rec.Body.String())
			}
			got := d.take()
			if rt.reaches != "" && (len(got) != 1 || got[0] != rt.reaches) {
				t.Errorf("editor: %d %q reached %v, want %s", rec.Code, rec.Body.String(), got, rt.reaches)
			}
		})
	}
}

// The read paths keep serving a viewer, and discard and unstage only clear
// the caller's own draft, so a demoted user can still drop leftovers.
func TestAuthorGateLeavesReadsAndCleanupOpen(t *testing.T) {
	d := &reachedDraft{}
	s := tenantServer(t, d, []string{"web"}, []string{"web"})
	rec := serve(s, http.MethodGet, "/api/permissions?namespace=web", "", viewerToken)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("viewer read refused: %q", rec.Body.String())
	}
	for _, rt := range []struct{ method, path, reaches string }{
		{http.MethodDelete, "/api/draft?project=team", "Discard"},
		{http.MethodDelete, "/api/draft/web/vm1", "Unstage"},
	} {
		rec := serve(s, rt.method, rt.path, "", viewerToken)
		if got := d.take(); rec.Code != http.StatusNoContent || len(got) != 1 || got[0] != rt.reaches {
			t.Errorf("viewer %s %s = %d %q, reached %v", rt.method, rt.path, rec.Code, rec.Body.String(), got)
		}
	}
}

// A project name never stands for the platform tier: only platformScope sets
// the bypass, and a namespace labelled with the reserved name resolves nowhere.
func TestPlatformNameIsNotABypass(t *testing.T) {
	d := &reachedDraft{}
	s := tenantServer(t, d, []string{"web", "sneaky"}, []string{"web", "sneaky"})
	for _, p := range s.AllProjects() {
		if p.Name == platformProjectName {
			t.Fatalf("resolver emitted the reserved name: %+v", p)
		}
	}
	c, err := s.clusterF.For(viewerToken)
	if err != nil {
		t.Fatal(err)
	}
	sc := scope{id: auth.Identity{Token: viewerToken}, cluster: c, proj: project.ProjectInfo{Name: platformProjectName, Namespaces: []string{"web"}}}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	for name, gate := range map[string]func(http.ResponseWriter) bool{
		"authorize":      func(w http.ResponseWriter) bool { return s.authorize(w, r, sc, verbUpdate, ssarVM, "web") },
		"authorizeDraft": func(w http.ResponseWriter) bool { return s.authorizeDraft(w, r, sc) },
		"authorizeRepo":  func(w http.ResponseWriter) bool { return s.authorizeRepo(w, r, sc) },
	} {
		if gate(httptest.NewRecorder()) {
			t.Errorf("%s let a project named %q through on its name", name, platformProjectName)
		}
	}
}

// The frame's signal: one cluster-wide allow answers every namespace; else
// each is reviewed; a failed review is unknown (absent), never false.
func TestAuthorNamespaces(t *testing.T) {
	s := scopeServer(eventbus.New())
	ctx := context.Background()
	nss := []string{"a", "b", "c"}

	t.Run("cluster-wide allow short-circuits", func(t *testing.T) {
		c, reviews := ssarCountingClient(func(a *authzv1.ResourceAttributes) bool { return a.Namespace == "" })
		got := s.authorNamespaces(ctx, auth.Identity{Token: "admin"}, c, nss)
		if *reviews != 1 || len(got) != 3 || !got["a"] || !got["b"] || !got["c"] {
			t.Fatalf("got %v after %d reviews, want all true after 1", got, *reviews)
		}
	})
	t.Run("per namespace", func(t *testing.T) {
		c, reviews := ssarCountingClient(func(a *authzv1.ResourceAttributes) bool { return a.Namespace == "b" })
		got := s.authorNamespaces(ctx, auth.Identity{Token: "tenant"}, c, nss)
		if *reviews != 4 || got["a"] || !got["b"] || got["c"] || len(got) != 3 {
			t.Fatalf("got %v after %d reviews", got, *reviews)
		}
	})
	t.Run("a failed review is unknown", func(t *testing.T) {
		kube := fake.NewSimpleClientset()
		kube.PrependReactor("create", "selfsubjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("timeout")
		})
		got := s.authorNamespaces(ctx, auth.Identity{Token: "flaky"}, cluster.NewClient(kube, nil, nil), nss)
		if len(got) != 0 {
			t.Fatalf("got %v, want no verdicts", got)
		}
	})
}

// A revert rewrites files of any namespace, so a project namespace the caller
// cannot even see refuses it, without naming that namespace.
func TestRevertNeedsEveryNamespace(t *testing.T) {
	d := &reachedDraft{}
	s := tenantServer(t, d, []string{"web", "hidden"}, []string{"web"})
	rec := serve(s, http.MethodPost, "/api/projects/team/revert", `{"hash":"`+hash40+`"}`, editorToken)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "hidden") {
		t.Fatalf("revert with a hidden namespace = %d %q, want an unnamed 403", rec.Code, rec.Body.String())
	}
	if got := d.take(); len(got) != 0 {
		t.Fatalf("revert reached the coordinator: %v", got)
	}
}

// The verdict is cached per (token, verb, resource, namespace): a different
// namespace or verb is its own review, a repeat is not, and a failed review is
// never cached as a denial.
func TestCanAuthorCacheKey(t *testing.T) {
	s := scopeServer(eventbus.New())
	fail := true
	c, reviews := ssarCountingClient(func(*authzv1.ResourceAttributes) bool { return true })
	id := auth.Identity{Token: "tok", Username: "u"}
	ctx := context.Background()

	for _, q := range []struct{ verb, ns string }{
		{verbUpdate, "a"}, {verbUpdate, "a"}, {verbUpdate, "b"}, {verbDelete, "a"},
	} {
		if ok, err := s.canAuthor(ctx, id, c, q.verb, ssarVM, q.ns); !ok || err != nil {
			t.Fatalf("%s in %s = %v, %v", q.verb, q.ns, ok, err)
		}
	}
	if *reviews != 3 {
		t.Fatalf("reviews = %d, want 3 (one per distinct verb and namespace)", *reviews)
	}

	probe := func() (bool, error) {
		if fail {
			return false, errors.New("apiserver down")
		}
		return true, nil
	}
	if _, err := s.ssarChecked(id, "k", probe); err == nil {
		t.Fatal("a failed review returned no error")
	}
	fail = false
	if ok, err := s.ssarChecked(id, "k", probe); !ok || err != nil {
		t.Fatalf("after recovery = %v, %v; the failure was cached", ok, err)
	}
}
