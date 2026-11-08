package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
		// The rule stays, named only to who may list its kind.
		name := map[bool]string{true: "anp", false: "cluster rule, priority 0"}[wantANP]
		if e.Gateway[0].Policy.Name != name || e.SNAT[0].Policy.Name != "eip" || e.SNAT[0].Policy.Namespaces == nil {
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

// "Add a boot image" registers the upload as a DataSource over its claim. A
// token without datasources create reads bootImageCreate false and the route
// refuses it before anything is made, so the field and the gate agree.
func TestUploadBootImage(t *testing.T) {
	g := newGateRig(t)
	var dv, ds map[string]any
	g.fc.on(http.MethodPost, "/apis/cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/datavolumes", echoCreate(&dv, nil))
	g.fc.on(http.MethodPost, "/apis/cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/datasources", echoCreate(&ds, nil))
	body := `{"namespace":"team-a-dev","name":"rhel-10","size":"20Gi","bootImage":true}`

	noDS := g.token(bootImageGrants[:2])
	if _, nc := g.caps(noDS, "team-a-dev"); nc.BootImageCreate || !nc.DataVolumeCreate {
		t.Errorf("without datasources create: bootImageCreate=%v dataVolumeCreate=%v", nc.BootImageCreate, nc.DataVolumeCreate)
	}
	wantStatus(t, do(g.s, noDS, http.MethodPost, "/api/uploads", body), http.StatusForbidden)
	if dv != nil || ds != nil {
		t.Fatalf("a refused boot image left objects behind: dv=%v ds=%v", dv, ds)
	}

	all := g.token(bootImageGrants)
	if _, nc := g.caps(all, "team-a-dev"); !nc.BootImageCreate {
		t.Error("with every right: bootImageCreate false")
	}
	rec := do(g.s, all, http.MethodPost, "/api/uploads", body)
	wantStatus(t, rec, http.StatusOK)
	if got := decodeBody[model.UploadTarget](t, rec); got.Image != "team-a-dev/rhel-10" {
		t.Errorf("target = %+v", got)
	}
	pvc := ds["spec"].(map[string]any)["source"].(map[string]any)["pvc"].(map[string]any)
	if dv == nil || pvc["name"] != "rhel-10" || pvc["namespace"] != "team-a-dev" {
		t.Errorf("datasource = %v", ds)
	}
}

// A refused upload, plain or as a boot image, makes nothing: without the
// upload-token right the DataVolume would only wait for bytes that never come.
func TestUploadRefusalMakesNothing(t *testing.T) {
	g := newGateRig(t)
	var made []string
	for _, res := range []string{"datavolumes", "datasources"} {
		g.fc.on(http.MethodPost, "/apis/cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/"+res, func(w http.ResponseWriter, r *http.Request) {
			made = append(made, res)
			echoCreate(nil, nil)(w, r)
		})
	}
	dvOnly := g.token(bootImageGrants[:1])
	if _, nc := g.caps(dvOnly, "team-a-dev"); nc.DataVolumeCreate || nc.BootImageCreate {
		t.Errorf("without upload tokens create: dataVolumeCreate=%v bootImageCreate=%v", nc.DataVolumeCreate, nc.BootImageCreate)
	}
	for _, body := range []string{
		`{"namespace":"team-a-dev","name":"disk","size":"1Gi"}`,
		`{"namespace":"team-a-dev","name":"disk","size":"1Gi","bootImage":true}`,
	} {
		wantStatus(t, do(g.s, dvOnly, http.MethodPost, "/api/uploads", body), http.StatusForbidden)
	}
	if len(made) != 0 {
		t.Errorf("a refused upload made %v", made)
	}
}

// A boot image from a URL or a registry: CDI downloads it inside the cluster,
// so it works with upload off. The source is checked before anything is
// made, a registry reference gains docker://, and the DataSource names the
// source without its query string.
func TestImports(t *testing.T) {
	fc := newFakeCluster(t)
	var dvs, dss []map[string]any
	record := func(into *[]map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var obj map[string]any
			echoCreate(&obj, nil)(w, r)
			*into = append(*into, obj)
		}
	}
	fc.on(http.MethodPost, "/apis/cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/datavolumes", record(&dvs))
	fc.on(http.MethodPost, "/apis/cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/datasources", record(&dss))
	s := fc.server(nil, Config{})

	const ok = `"namespace":"team-a-dev","name":"noble","size":"20Gi"`
	for body, want := range map[string]int{
		`{"namespace":"team-a-dev","name":"noble","kind":"url","source":"https://a.example/x.img"}`:               http.StatusBadRequest,
		`{` + ok + `,"kind":"url","source":"ftp://a.example/x.img"}`:                                              http.StatusBadRequest,
		`{` + ok + `,"kind":"url","source":"https://ops:pw@a.example/x.img"}`:                                     http.StatusBadRequest,
		`{` + ok + `,"kind":"url","source":"https://a.example/x y.img"}`:                                          http.StatusBadRequest,
		`{` + ok + `,"kind":"url","source":""}`:                                                                   http.StatusBadRequest,
		`{` + ok + `,"kind":"s3","source":"https://a.example/x.img"}`:                                             http.StatusBadRequest,
		`{` + ok + `,"kind":"registry","source":"https://quay.io/x/y:1"}`:                                         http.StatusBadRequest,
		`{"namespace":"team-a-dev","name":"Noble","size":"20Gi","kind":"url","source":"https://a.example/x.img"}`: http.StatusBadRequest,
		`{"namespace":"team-a-dev","name":"noble","size":"lots","kind":"url","source":"https://a.example/x.img"}`: http.StatusBadRequest,
		`{"namespace":"elsewhere","name":"noble","size":"20Gi","kind":"url","source":"https://a.example/x.img"}`:  http.StatusNotFound,
	} {
		if rec := do(s, adminToken, http.MethodPost, "/api/imports", body); rec.Code != want {
			t.Errorf("import %s = %d %q, want %d", body, rec.Code, rec.Body.String(), want)
		} else if strings.Contains(rec.Body.String(), "a.example") {
			t.Errorf("import %s: the refusal quotes the source: %q", body, rec.Body.String())
		}
	}
	if len(dvs)+len(dss) != 0 {
		t.Fatalf("a refused import made objects: %v %v", dvs, dss)
	}

	rec := do(s, adminToken, http.MethodPost, "/api/imports", `{`+ok+`,"kind":"url","source":"https://images.example/noble.img?X-Amz-Signature=abc"}`)
	wantStatus(t, rec, http.StatusOK)
	if got := decodeBody[model.UploadTarget](t, rec); got.Image != "team-a-dev/noble" {
		t.Errorf("target = %+v", got)
	}
	rec = do(s, adminToken, http.MethodPost, "/api/imports", `{"namespace":"team-a-dev","name":"fedora","size":"30Gi","storageClass":"fast","kind":"registry","source":"quay.io/containerdisks/fedora:42"}`)
	wantStatus(t, rec, http.StatusOK)
	if len(dvs) != 2 || len(dss) != 2 {
		t.Fatalf("made %d data volumes, %d data sources; want 2 each", len(dvs), len(dss))
	}
	source := func(dv map[string]any) map[string]any { return dv["spec"].(map[string]any)["source"].(map[string]any) }
	if got := source(dvs[0])["http"].(map[string]any)["url"]; got != "https://images.example/noble.img?X-Amz-Signature=abc" {
		t.Errorf("http source = %v, want the url as given", got)
	}
	if got := source(dvs[1])["registry"].(map[string]any)["url"]; got != "docker://quay.io/containerdisks/fedora:42" {
		t.Errorf("registry source = %v, want docker:// added", got)
	}
	ann := func(ds map[string]any) map[string]any {
		return ds["metadata"].(map[string]any)["annotations"].(map[string]any)
	}
	if got := ann(dss[0]); got["dotvirt.io/import-kind"] != "url" || got["dotvirt.io/import-source"] != "https://images.example/noble.img" {
		t.Errorf("datasource annotations = %v", got)
	}
	if got := ann(dss[1])["dotvirt.io/import-source"]; got != "quay.io/containerdisks/fedora:42" {
		t.Errorf("registry datasource source = %v", got)
	}
}

// A refused import makes nothing; it asks no upload token, which an import
// never mints.
func TestImportRefusalMakesNothing(t *testing.T) {
	g := newGateRig(t)
	var made []string
	for _, res := range []string{"datavolumes", "datasources"} {
		g.fc.on(http.MethodPost, "/apis/cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/"+res, func(w http.ResponseWriter, r *http.Request) {
			made = append(made, res)
			echoCreate(nil, nil)(w, r)
		})
	}
	body := `{"namespace":"team-a-dev","name":"noble","size":"20Gi","kind":"url","source":"https://images.example/noble.img"}`
	for i := range importGrants {
		tok := g.token(without(importGrants, i))
		if _, nc := g.caps(tok, "team-a-dev"); nc.BootImageImport {
			t.Errorf("without %v: bootImageImport true", importGrants[i])
		}
		wantStatus(t, do(g.s, tok, http.MethodPost, "/api/imports", body), http.StatusForbidden)
	}
	if len(made) != 0 {
		t.Errorf("a refused import made %v", made)
	}
	tok := g.token(importGrants)
	if _, nc := g.caps(tok, "team-a-dev"); !nc.BootImageImport || nc.BootImageCreate {
		t.Errorf("with the import rights alone: bootImageImport=%v bootImageCreate=%v", nc.BootImageImport, nc.BootImageCreate)
	}
	wantStatus(t, do(g.s, tok, http.MethodPost, "/api/imports", body), http.StatusOK)
	if len(made) != 2 {
		t.Errorf("made %v, want a data volume and a data source", made)
	}
}

// unfinishedImage answers the DataSource noble in team-a-dev and its
// DataVolume, in the shape dotvirt makes, and records each delete of them in
// order; failing names a resource path whose delete is forbidden.
func unfinishedImage(fc *fakeCluster, ready bool, failing string) *[]string {
	const cdi = "/apis/cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/"
	status := "False"
	if ready {
		status = "True"
	}
	fc.on(http.MethodGet, cdi+"datasources/noble", func(w http.ResponseWriter, _ *http.Request) {
		writeObj(w, map[string]any{
			"apiVersion": "cdi.kubevirt.io/v1beta1", "kind": "DataSource",
			"metadata": map[string]any{"name": "noble", "namespace": "team-a-dev", "resourceVersion": "7"},
			"spec":     map[string]any{"source": map[string]any{"pvc": map[string]any{"name": "noble", "namespace": "team-a-dev"}}},
			"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": status}}},
		})
	})
	fc.on(http.MethodGet, cdi+"datavolumes/noble", func(w http.ResponseWriter, _ *http.Request) {
		writeObj(w, map[string]any{
			"apiVersion": "cdi.kubevirt.io/v1beta1", "kind": "DataVolume",
			"metadata": map[string]any{"name": "noble", "namespace": "team-a-dev", "uid": "dv-uid"},
			"spec":     map[string]any{"source": map[string]any{"http": map[string]any{"url": "https://images.example/noble.img"}}},
		})
	})
	var deleted []string
	for _, res := range []string{cdi + "datasources", cdi + "datavolumes"} {
		fc.on(http.MethodDelete, res+"/noble", func(w http.ResponseWriter, _ *http.Request) {
			if res == failing {
				writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
				return
			}
			deleted = append(deleted, res[strings.LastIndex(res, "/")+1:])
			writeObj(w, metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess})
		})
	}
	return &deleted
}

// A refused delete removes nothing: both rights are asked whole before the
// first delete, and the capability reads false without either.
func TestBootImageDeleteRefusalDeletesNothing(t *testing.T) {
	g := newGateRig(t)
	deleted := unfinishedImage(g.fc, false, "")
	for i := range deleteGrants {
		tok := g.token(without(deleteGrants, i))
		if _, nc := g.caps(tok, "team-a-dev"); nc.BootImageDelete {
			t.Errorf("without %v: bootImageDelete true", deleteGrants[i])
		}
		wantStatus(t, do(g.s, tok, http.MethodDelete, "/api/images/team-a-dev/noble", ""), http.StatusForbidden)
	}
	if len(*deleted) != 0 {
		t.Errorf("a refused delete removed %v", *deleted)
	}
	tok := g.token(deleteGrants)
	if _, nc := g.caps(tok, "team-a-dev"); !nc.BootImageDelete {
		t.Error("with the delete rights: bootImageDelete false")
	}
	wantStatus(t, do(g.s, tok, http.MethodDelete, "/api/images/team-a-dev/noble", ""), http.StatusOK)
	if want := []string{"datasources", "datavolumes"}; !reflect.DeepEqual(*deleted, want) {
		t.Errorf("deleted %v, want %v", *deleted, want)
	}
}

// The route refuses a ready boot image with the reason and removes nothing;
// an unfinished one goes, and the shared catalog is read again so New VM and
// the Images tab drop it at once. A delete that stops partway says what
// remains, with the apiserver's status.
func TestBootImageDelete(t *testing.T) {
	fc := newFakeCluster(t)
	deleted := unfinishedImage(fc, true, "")
	s := fc.server(nil, Config{})
	rec := do(s, adminToken, http.MethodDelete, "/api/images/team-a-dev/noble", "")
	wantStatus(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "noble is ready") || len(*deleted) != 0 {
		t.Errorf("ready: %q, deleted %v", rec.Body.String(), *deleted)
	}
	wantStatus(t, do(s, adminToken, http.MethodDelete, "/api/images/elsewhere/noble", ""), http.StatusNotFound)

	fc = newFakeCluster(t)
	deleted = unfinishedImage(fc, false, "")
	s = fc.server(nil, Config{})
	s.options.Put("all", model.Options{OSImages: []model.OSImage{{Name: "noble", Namespace: "team-a-dev"}}})
	rec = do(s, adminToken, http.MethodDelete, "/api/images/team-a-dev/noble", "")
	wantStatus(t, rec, http.StatusOK)
	if got := decodeBody[model.BootImageRemoval](t, rec); got.Note != "" || len(*deleted) != 2 {
		t.Errorf("removal %+v, deleted %v; want both, no note", got, *deleted)
	}
	if _, ok := s.options.Get("all"); ok {
		t.Error("the catalog cache still holds the deleted boot image")
	}

	fc = newFakeCluster(t)
	unfinishedImage(fc, false, "/apis/cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/datavolumes")
	s = fc.server(nil, Config{})
	rec = do(s, adminToken, http.MethodDelete, "/api/images/team-a-dev/noble", "")
	wantStatus(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "removed the boot image noble; its data volume remains") {
		t.Errorf("partial: %q", rec.Body.String())
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

// The cluster signal is the routes' first check: no proxy, no upload anywhere.
func TestImageUploadCapabilityFollowsTheProxy(t *testing.T) {
	for proxy, want := range map[string]bool{"": false, "https://cdi-uploadproxy.example": true} {
		s := newFakeCluster(t).server(nil, Config{UploadProxyURL: proxy})
		rec := do(s, adminToken, http.MethodGet, "/api/capabilities", "")
		wantStatus(t, rec, http.StatusOK)
		if cl := decodeBody[model.Capabilities](t, rec).Cluster; cl == nil || cl.ImageUpload != want {
			t.Errorf("proxy %q: imageUpload %+v, want %v", proxy, cl, want)
		}
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

// A capacity segment names the node it sits on: a caller who may read nodes
// sees the per-node split, anyone else the summed free capacity alone, and the
// shared cached sheet keeps its segments for the next node reader.
func TestStorageClassesSegmentsNeedNodeRead(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodGet, "/apis/storage.k8s.io/v1/storageclasses", unstructuredList("storage.k8s.io/v1", "StorageClass",
		map[string]any{"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": map[string]any{"name": "local"}, "provisioner": "topolvm.io"}))
	capacity := func(node, free string) map[string]any {
		return map[string]any{
			"apiVersion": "storage.k8s.io/v1", "kind": "CSIStorageCapacity",
			"metadata":         map[string]any{"name": "cap-" + node, "namespace": "openshift-storage"},
			"storageClassName": "local",
			"nodeTopology":     map[string]any{"matchLabels": map[string]any{"topology.topolvm.io/node": node}},
			"capacity":         free,
		}
	}
	fc.on(http.MethodGet, "/apis/storage.k8s.io/v1/csistoragecapacities", unstructuredList("storage.k8s.io/v1", "CSIStorageCapacity",
		capacity("host-01", "1Ti"), capacity("host-02", "3Ti")))
	s := fc.server(nil, Config{})

	for _, tc := range []struct {
		token    string
		segments int
	}{{tenantToken, 0}, {adminToken, 2}, {tenantToken, 0}} {
		rec := do(s, tc.token, http.MethodGet, "/api/storage/classes", "")
		wantStatus(t, rec, http.StatusOK)
		got := decodeBody[[]model.StorageClassInfo](t, rec)
		if len(got) != 1 || got[0].Free == nil || *got[0].Free != 4<<40 || len(got[0].Segments) != tc.segments {
			t.Fatalf("%s: classes = %+v", tc.token, got)
		}
		if tc.segments == 0 && strings.Contains(rec.Body.String(), "host-0") {
			t.Errorf("%s: node names leaked: %s", tc.token, rec.Body.String())
		}
	}
}

// An SA-side read failure is dotvirt's, never the caller's: a 503 naming no
// internals, and above all never a 401, which would sign the caller out.
func TestStorageClassesFailure(t *testing.T) {
	for _, c := range []struct {
		code   int
		reason metav1.StatusReason
	}{
		{http.StatusInternalServerError, metav1.StatusReasonInternalError},
		{http.StatusUnauthorized, metav1.StatusReasonUnauthorized},
	} {
		fc := newFakeCluster(t)
		fc.on(http.MethodGet, "/apis/storage.k8s.io/v1/storageclasses", func(w http.ResponseWriter, r *http.Request) {
			writeStatus(w, c.code, c.reason)
		})
		s := fc.server(nil, Config{})
		rec := do(s, adminToken, http.MethodGet, "/api/storage/classes", "")
		wantStatus(t, rec, http.StatusServiceUnavailable)
		if strings.Contains(rec.Body.String(), "storageclasses") {
			t.Errorf("internal detail leaked: %q", rec.Body.String())
		}
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

// The cluster-wide snapshot listing reads every managed namespace under the
// caller's token, carries the source VM and captured volumes, and skips a
// repoless project's namespace like the events lane.
func TestStorageSnapshots(t *testing.T) {
	fc := newFakeCluster(t)
	snap := func(name, vm, created string, volumes ...any) map[string]any {
		return map[string]any{
			"apiVersion": "snapshot.kubevirt.io/v1beta1", "kind": "VirtualMachineSnapshot",
			"metadata": map[string]any{"name": name, "namespace": "team-a-dev", "creationTimestamp": created},
			"spec":     map[string]any{"source": map[string]any{"kind": "VirtualMachine", "name": vm}},
			"status": map[string]any{"phase": "Succeeded", "readyToUse": true,
				"snapshotVolumes": map[string]any{"includedVolumes": volumes}},
		}
	}
	fc.on(http.MethodGet, "/apis/snapshot.kubevirt.io/v1beta1/namespaces/team-a-dev/virtualmachinesnapshots", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != adminToken {
			t.Errorf("snapshots listed as %q, want the caller", bearer(r))
		}
		unstructuredList("snapshot.kubevirt.io/v1beta1", "VirtualMachineSnapshotList",
			snap("web-old", "web", "2026-01-01T00:00:00Z", "rootdisk"),
			snap("db-new", "db", "2026-01-03T00:00:00Z", "rootdisk", "data"))(w, r)
	})
	s := fc.server(nil, Config{})

	rec := do(s, adminToken, http.MethodGet, "/api/storage/snapshots", "")
	wantStatus(t, rec, http.StatusOK)
	got := decodeBody[[]model.Snapshot](t, rec)
	if len(got) != 2 || got[0].Name != "db-new" || got[0].VM != "db" || got[0].Namespace != "team-a-dev" ||
		len(got[0].Volumes) != 2 || got[1].Volumes[0] != "rootdisk" {
		t.Errorf("snapshots = %+v", got)
	}
	if fc.called(http.MethodGet, "/apis/snapshot.kubevirt.io/v1beta1/namespaces/orphans-ns/virtualmachinesnapshots") {
		t.Error("snapshots listed in a repoless project's namespace")
	}
	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/storage/snapshots?project=nope", ""), http.StatusNotFound)
	// A tenant who sees no project namespace lists nothing at all.
	rec = do(s, tenantToken, http.MethodGet, "/api/storage/snapshots", "")
	wantStatus(t, rec, http.StatusOK)
	if got := decodeBody[[]model.Snapshot](t, rec); len(got) != 0 {
		t.Errorf("tenant snapshots = %+v", got)
	}
}

func TestStorageSnapshotsForbidden(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodGet, "/apis/snapshot.kubevirt.io/v1beta1/namespaces/team-a-dev/virtualmachinesnapshots", func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
	})
	s := fc.server(nil, Config{})
	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/storage/snapshots", ""), http.StatusForbidden)
}

// A claim reports the host the scheduler bound it to, so a stopped VM's
// local disk still has a host.
func TestStorageClaims(t *testing.T) {
	fc := newFakeCluster(t)
	class := "lvms-vgslow"
	fc.on(http.MethodGet, "/api/v1/namespaces/team-a-dev/persistentvolumeclaims", func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, corev1.PersistentVolumeClaimList{
			TypeMeta: metav1.TypeMeta{Kind: "PersistentVolumeClaimList", APIVersion: "v1"},
			Items: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "web-rootdisk", Namespace: "team-a-dev",
					Annotations: map[string]string{"volume.kubernetes.io/selected-node": "host-07"}},
				Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class},
				Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound,
					Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("30Gi")}},
			}},
		})
	})
	s := fc.server(nil, Config{})
	rec := do(s, adminToken, http.MethodGet, "/api/storage/claims?project=team-a", "")
	wantStatus(t, rec, http.StatusOK)
	got := decodeBody[[]model.Claim](t, rec)
	want := model.Claim{Namespace: "team-a-dev", Name: "web-rootdisk", Node: "host-07", StorageClass: class, Capacity: "30Gi", Phase: "Bound"}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("claims = %+v", got)
	}
}
