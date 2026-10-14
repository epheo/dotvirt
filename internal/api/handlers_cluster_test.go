package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
)

func node(name string, unschedulable, maintenance, ready bool) corev1.Node {
	n := corev1.Node{
		TypeMeta:   metav1.TypeMeta{Kind: "Node", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{}},
		Spec:       corev1.NodeSpec{Unschedulable: unschedulable},
	}
	if maintenance {
		n.Annotations["dotvirt.io/maintenance"] = "true"
	}
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}
	return n
}

func TestNodes(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodGet, "/api/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("labelSelector") != "kubevirt.io/schedulable=true" {
			t.Errorf("node list selector = %q", r.URL.Query().Get("labelSelector"))
		}
		writeObj(w, corev1.NodeList{TypeMeta: metav1.TypeMeta{Kind: "NodeList", APIVersion: "v1"}, Items: []corev1.Node{
			node("worker-b", true, true, true), node("worker-a", false, false, false),
		}})
	})
	fc.on(http.MethodGet, "/api/v1/nodes/worker-b", func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, node("worker-b", true, true, true))
	})
	s := fc.server(nil, Config{})

	rec := do(s, adminToken, http.MethodGet, "/api/nodes", "")
	wantStatus(t, rec, http.StatusOK)
	got := decodeBody[[]model.Node](t, rec)
	if len(got) != 2 || got[0].Name != "worker-a" || got[0].Ready || !got[1].Maintenance || !got[1].Unschedulable || !got[1].Ready {
		t.Errorf("nodes = %+v", got)
	}

	rec = do(s, adminToken, http.MethodGet, "/api/nodes/worker-b", "")
	wantStatus(t, rec, http.StatusOK)
	if info := decodeBody[model.NodeInfo](t, rec); !info.Unschedulable || !info.Maintenance || !info.CanCordon {
		t.Errorf("admin node info = %+v", info)
	}
	rec = do(s, tenantToken, http.MethodGet, "/api/nodes/worker-b", "")
	wantStatus(t, rec, http.StatusOK)
	if info := decodeBody[model.NodeInfo](t, rec); info.CanCordon {
		t.Errorf("tenant may cordon: %+v", info)
	}
	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/nodes/ghost", ""), http.StatusNotFound)
}

func TestNodesForbidden(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodGet, "/api/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
	})
	s := fc.server(nil, Config{})
	wantStatus(t, do(s, tenantToken, http.MethodGet, "/api/nodes", ""), http.StatusForbidden)
}

func TestNodeCordonAndMaintenance(t *testing.T) {
	fc := newFakeCluster(t)
	var patches []string
	fc.on(http.MethodPatch, "/api/v1/nodes/worker-a", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		patches = append(patches, string(b))
		writeObj(w, node("worker-a", false, false, true))
	})
	fc.on(http.MethodPatch, "/api/v1/nodes/locked", func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
	})
	s := fc.server(nil, Config{})

	for _, c := range []struct{ route, body, patch string }{
		{"cordon", `{"unschedulable":true}`, `{"spec":{"unschedulable":true}}`},
		{"cordon", `{"unschedulable":false}`, `{"spec":{"unschedulable":false}}`},
		{"maintenance", `{"enter":true}`, `{"metadata":{"annotations":{"dotvirt.io/maintenance":"true"}},"spec":{"unschedulable":true}}`},
		{"maintenance", `{"enter":false}`, `{"metadata":{"annotations":{"dotvirt.io/maintenance":null}},"spec":{"unschedulable":false}}`},
	} {
		patches = nil
		wantStatus(t, do(s, adminToken, http.MethodPost, "/api/nodes/worker-a/"+c.route, c.body), http.StatusNoContent)
		if len(patches) != 1 || patches[0] != c.patch {
			t.Errorf("%s %s patched %q, want %q", c.route, c.body, patches, c.patch)
		}
	}
	for _, route := range []string{"cordon", "maintenance"} {
		wantStatus(t, do(s, adminToken, http.MethodPost, "/api/nodes/worker-a/"+route, ""), http.StatusBadRequest)
		wantStatus(t, do(s, tenantToken, http.MethodPost, "/api/nodes/locked/"+route, `{}`), http.StatusForbidden)
	}
}

// Evacuation reads its candidates from the SA snapshot (every running VM on
// the node, tracked or not), skips moves already in flight, and reports
// per-VM failures sorted, instead of dropping them.
func TestNodeEvacuate(t *testing.T) {
	fc := newFakeCluster(t)
	vmi := func(ns, name, node string, phase kubevirtcorev1.VirtualMachineInstancePhase, mig *kubevirtcorev1.VirtualMachineInstanceMigrationState) kubevirtcorev1.VirtualMachineInstance {
		return kubevirtcorev1.VirtualMachineInstance{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Status:     kubevirtcorev1.VirtualMachineInstanceStatus{Phase: phase, NodeName: node, MigrationState: mig},
		}
	}
	fc.vmis = []kubevirtcorev1.VirtualMachineInstance{
		vmi("team-a-dev", "web", "worker-a", kubevirtcorev1.Running, nil),
		vmi("untracked", "legacy", "worker-a", kubevirtcorev1.Running, nil),
		vmi("untracked", "zz-denied", "worker-a", kubevirtcorev1.Running, nil),
		vmi("team-a-dev", "moving", "worker-a", kubevirtcorev1.Running, &kubevirtcorev1.VirtualMachineInstanceMigrationState{}),
		vmi("team-a-dev", "booting", "worker-a", kubevirtcorev1.Scheduling, nil),
		vmi("team-a-dev", "elsewhere", "worker-b", kubevirtcorev1.Running, nil),
	}
	fc.on(http.MethodGet, "/api/v1/nodes/worker-a", func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, node("worker-a", true, true, true))
	})
	var migrated atomic.Int32
	ok := func(w http.ResponseWriter, r *http.Request) { migrated.Add(1); w.WriteHeader(http.StatusAccepted) }
	fc.on(http.MethodPut, "/apis/subresources.kubevirt.io/v1/namespaces/team-a-dev/virtualmachines/web/migrate", ok)
	fc.on(http.MethodPut, "/apis/subresources.kubevirt.io/v1/namespaces/untracked/virtualmachines/legacy/migrate", ok)
	fc.on(http.MethodPut, "/apis/subresources.kubevirt.io/v1/namespaces/untracked/virtualmachines/zz-denied/migrate", func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
	})
	s := fc.server(nil, Config{})

	// Without node-update authority the node's VMs are never enumerated.
	wantStatus(t, do(s, tenantToken, http.MethodPost, "/api/nodes/worker-a/evacuate", ""), http.StatusForbidden)
	if migrated.Load() != 0 {
		t.Fatal("a tenant evacuation migrated VMs")
	}

	rec := do(s, adminToken, http.MethodPost, "/api/nodes/worker-a/evacuate", "")
	wantStatus(t, rec, http.StatusOK)
	ev := decodeBody[model.Evacuation](t, rec)
	if ev.Requested != 2 || ev.Skipped != 1 || len(ev.Failures) != 1 || ev.Failures[0].Name != "zz-denied" {
		t.Errorf("evacuation = %+v, want 2 requested, 1 skipped, zz-denied failed", ev)
	}
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/nodes/ghost/evacuate", ""), http.StatusNotFound)
}

func TestEffectivePolicy(t *testing.T) {
	fc := newFakeCluster(t)
	tmpl := &kubevirtcorev1.VirtualMachineInstanceTemplateSpec{}
	tmpl.ObjectMeta.Labels = map[string]string{"app": "web"}
	fc.vms = []kubevirtcorev1.VirtualMachine{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a-dev", Name: "web"},
		Spec:       kubevirtcorev1.VirtualMachineSpec{Template: tmpl},
	}}
	s := fc.server(nil, Config{})

	rec := do(s, adminToken, http.MethodGet, vmPath+"/policy", "")
	wantStatus(t, rec, http.StatusOK)
	eff := decodeBody[model.EffectivePolicy](t, rec)
	if eff.Namespace != "team-a-dev" || eff.VM != "web" || eff.Labels["app"] != "web" || eff.LabelsLive {
		t.Errorf("vm policy = %+v, want the manifest labels", eff)
	}
	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/vms/team-a-dev/ghost/policy", ""), http.StatusNotFound)

	rec = do(s, adminToken, http.MethodGet, "/api/namespaces/team-a-dev/policy", "")
	wantStatus(t, rec, http.StatusOK)
	if eff := decodeBody[model.EffectivePolicy](t, rec); eff.Namespace != "team-a-dev" || eff.VM != "" {
		t.Errorf("namespace policy = %+v", eff)
	}
}

// A cluster-tier policy's subject namespaces reach only callers holding the
// kind's create authority; the rule itself is never hidden.
func TestRedactEffective(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		return token == adminToken || a.Resource == "egressips"
	}
	s := fc.server(nil, Config{})
	f := s.clusterF
	eff := func() model.EffectivePolicy {
		return model.EffectivePolicy{
			Gateway: []model.PolicyBinding{{Policy: model.Policy{Kind: model.PolicyAdmin, Name: "anp", Namespaces: []string{"other-tenant"}}}},
			SNAT:    []model.PolicyBinding{{Policy: model.Policy{Kind: model.PolicyEgressIP, Name: "eip", Namespaces: []string{"other-tenant"}}}},
		}
	}
	for token, wantANP := range map[string]bool{adminToken: true, tenantToken: false} {
		c, err := f.For(token)
		if err != nil {
			t.Fatal(err)
		}
		e := eff()
		s.redactEffective(t.Context(), scope{id: auth.Identity{Token: token}, cluster: c}, &e)
		if (e.Gateway[0].Policy.Namespaces != nil) != wantANP {
			t.Errorf("%s: admin policy namespaces = %v", token, e.Gateway[0].Policy.Namespaces)
		}
		if e.Gateway[0].Policy.Name != "anp" || e.SNAT[0].Policy.Namespaces == nil {
			t.Errorf("%s: redaction dropped a rule or an authorized kind: %+v", token, e)
		}
	}
}

func TestUploads(t *testing.T) {
	fc := newFakeCluster(t)
	dvPath := "/apis/cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/datavolumes"
	var dv map[string]any
	fc.on(http.MethodPost, dvPath, echoCreate(&dv, nil))
	fc.on(http.MethodGet, dvPath+"/disk", func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, map[string]any{
			"apiVersion": "cdi.kubevirt.io/v1beta1", "kind": "DataVolume",
			"metadata": map[string]any{"name": "disk", "namespace": "team-a-dev"},
			"status":   map[string]any{"phase": "UploadReady", "progress": "N/A"},
		})
	})
	tokPath := "/apis/upload.cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/uploadtokenrequests"
	fc.on(http.MethodPost, tokPath, echoCreate(nil, map[string]any{"token": "tok-123"}))
	s := fc.server(nil, Config{UploadProxyURL: "https://cdi-uploadproxy.example/"})

	for body, want := range map[string]int{
		`{"namespace":"team-a-dev","name":"disk"}`:              http.StatusBadRequest,
		`{"namespace":"elsewhere","name":"disk","size":"10Gi"}`: http.StatusNotFound,
		`not json`: http.StatusBadRequest,
	} {
		if rec := do(s, adminToken, http.MethodPost, "/api/uploads", body); rec.Code != want {
			t.Errorf("create %s = %d, want %d", body, rec.Code, want)
		}
	}
	rec := do(s, adminToken, http.MethodPost, "/api/uploads", `{"namespace":"team-a-dev","name":"disk","size":"10Gi","storageClass":"fast"}`)
	wantStatus(t, rec, http.StatusOK)
	if got := decodeBody[model.UploadTarget](t, rec); got.Namespace != "team-a-dev" || got.Name != "disk" {
		t.Errorf("target = %+v", got)
	}
	storage := dv["spec"].(map[string]any)["storage"].(map[string]any)
	if storage["storageClassName"] != "fast" || storage["resources"].(map[string]any)["requests"].(map[string]any)["storage"] != "10Gi" {
		t.Errorf("datavolume storage = %v", storage)
	}

	rec = do(s, adminToken, http.MethodGet, "/api/uploads/team-a-dev/disk", "")
	wantStatus(t, rec, http.StatusOK)
	if st := decodeBody[model.UploadStatus](t, rec); !st.Ready || st.Phase != "UploadReady" {
		t.Errorf("status = %+v", st)
	}

	rec = do(s, adminToken, http.MethodPost, "/api/uploads/team-a-dev/disk/token", "")
	wantStatus(t, rec, http.StatusOK)
	tok := decodeBody[model.UploadToken](t, rec)
	if tok.Token != "tok-123" || tok.UploadURL != "https://cdi-uploadproxy.example/v1beta1/upload-async" {
		t.Errorf("token = %+v", tok)
	}
}

// A token request the proxy answers without a token is an internal failure,
// never an empty token handed to the browser.
func TestUploadTokenNotIssued(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodPost, "/apis/upload.cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/uploadtokenrequests", echoCreate(nil, nil))
	s := fc.server(nil, Config{UploadProxyURL: "https://cdi-uploadproxy.example"})
	rec := do(s, adminToken, http.MethodPost, "/api/uploads/team-a-dev/disk/token", "")
	wantStatus(t, rec, http.StatusInternalServerError)
}

func TestUploadsDisabled(t *testing.T) {
	fc := newFakeCluster(t)
	s := fc.server(nil, Config{})
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/uploads", `{"namespace":"team-a-dev","name":"d","size":"1Gi"}`), http.StatusServiceUnavailable)
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/uploads/team-a-dev/d/token", ""), http.StatusServiceUnavailable)
	if calls := fc.seen(); len(calls) != 0 {
		t.Errorf("disabled upload reached the cluster: %v", calls)
	}
}

// The class sheet is read with the SA once and shared; a later caller is a
// cache hit, and optional sources that 404 never fail the list.
func TestStorageClasses(t *testing.T) {
	fc := newFakeCluster(t)
	var lists atomic.Int32
	fc.on(http.MethodGet, "/apis/storage.k8s.io/v1/storageclasses", func(w http.ResponseWriter, r *http.Request) {
		lists.Add(1)
		if bearer(r) != "sa-token" {
			t.Errorf("storage classes read as %q, want the SA", bearer(r))
		}
		unstructuredList("storage.k8s.io/v1", "StorageClass", map[string]any{
			"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass",
			"metadata":    map[string]any{"name": "fast", "annotations": map[string]any{"storageclass.kubernetes.io/is-default-class": "true"}},
			"provisioner": "csi.example", "allowVolumeExpansion": true,
		})(w, r)
	})
	s := fc.server(nil, Config{})

	for _, token := range []string{adminToken, tenantToken} {
		rec := do(s, token, http.MethodGet, "/api/storage/classes", "")
		wantStatus(t, rec, http.StatusOK)
		got := decodeBody[[]model.StorageClassInfo](t, rec)
		if len(got) != 1 || got[0].Name != "fast" || !got[0].Default || !got[0].Expandable || got[0].Provisioner != "csi.example" {
			t.Errorf("%s: classes = %+v", token, got)
		}
	}
	if lists.Load() != 1 {
		t.Errorf("storage classes listed %d times, want 1", lists.Load())
	}
}

func TestStorageClassesFailure(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodGet, "/apis/storage.k8s.io/v1/storageclasses", func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusInternalServerError, metav1.StatusReasonInternalError)
	})
	s := fc.server(nil, Config{})
	rec := do(s, adminToken, http.MethodGet, "/api/storage/classes", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "storageclasses") {
		t.Errorf("internal detail leaked: %q", rec.Body.String())
	}
}

func TestFinishSSO(t *testing.T) {
	fc := newFakeCluster(t)
	var applied map[string]any
	fc.on(http.MethodPatch, "/apis/oauth.openshift.io/v1/oauthclients/dotvirt", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != adminToken {
			writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/apply-patch+yaml" {
			t.Errorf("content type = %q, want server-side apply", ct)
		}
		_ = json.NewDecoder(r.Body).Decode(&applied)
		writeObj(w, applied)
	})
	s := fc.server(nil, Config{})
	finish := func(token string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleFinishSSO(rec, asUser(httptest.NewRequest(http.MethodPost, "/api/auth/oauthclient", nil), token))
		return rec
	}

	wantStatus(t, finish(adminToken), http.StatusNotFound) // SSO not enabled

	o, err := auth.NewOAuth(auth.OAuthConfig{ClientID: "dotvirt", ClientSecret: "s3cret", RedirectURL: "https://dotvirt.example/api/auth/callback"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.oauth = o

	rec := finish(tenantToken)
	wantStatus(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "cluster admin") {
		t.Errorf("forbidden message = %q, want the next action", rec.Body.String())
	}

	wantStatus(t, finish(adminToken), http.StatusNoContent)
	if applied["secret"] != "s3cret" || applied["redirectURIs"].([]any)[0] != "https://dotvirt.example/api/auth/callback" {
		t.Errorf("applied = %v", applied)
	}
	if !o.ClientRegistered(t.Context()) {
		t.Error("a successful apply left SSO pending")
	}
}
