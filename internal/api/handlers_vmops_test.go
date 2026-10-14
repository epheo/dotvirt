package api

import (
	"io"
	"net/http"
	"strings"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
)

const vmPath = "/api/vms/team-a-dev/web"

// A VM route names a namespace the caller cannot see: 404 before any cluster
// call, so a tenant cannot probe objects outside their projects.
func TestVMRoutesOutsideVisibilityAreNotFound(t *testing.T) {
	fc := newFakeCluster(t)
	s := fc.server(nil, Config{})
	routes := []struct{ method, path, body string }{
		{http.MethodGet, vmPath + "/events", ""},
		{http.MethodGet, vmPath + "/screenshot", ""},
		{http.MethodGet, vmPath + "/snapshots", ""},
		{http.MethodPost, vmPath + "/snapshots", ""},
		{http.MethodPost, vmPath + "/snapshots/s1/restore", ""},
		{http.MethodDelete, vmPath + "/snapshots/s1", ""},
		{http.MethodGet, vmPath + "/clones", ""},
		{http.MethodPost, vmPath + "/clone", `{"target":"copy"}`},
		{http.MethodPost, vmPath + "/restart", ""},
		{http.MethodPost, vmPath + "/migrate", ""},
		{http.MethodPost, vmPath + "/pause", ""},
		{http.MethodPost, vmPath + "/unpause", ""},
		{http.MethodGet, vmPath + "/policy", ""},
		{http.MethodGet, "/api/namespaces/team-a-dev/policy", ""},
		{http.MethodGet, "/api/uploads/team-a-dev/disk", ""},
		{http.MethodGet, "/api/permissions?namespace=team-a-dev", ""},
	}
	for _, rt := range routes {
		rec := do(s, tenantToken, rt.method, rt.path, rt.body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d %q, want 404", rt.method, rt.path, rec.Code, rec.Body.String())
		}
	}
	if calls := fc.seen(); len(calls) != 0 {
		t.Errorf("unauthorized requests reached the cluster: %v", calls)
	}
}

// No identity in context: the handler fails closed (503 names only the
// subsystem), never runs as some default identity.
func TestVMRoutesWithoutIdentity(t *testing.T) {
	fc := newFakeCluster(t)
	s := fc.server(nil, Config{})
	for _, p := range []string{vmPath + "/events", "/api/nodes", "/api/events", "/api/templates", "/api/drs", "/api/storage/classes"} {
		rec := do(s, "", http.MethodGet, p, "")
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s without identity = %d, want 503", p, rec.Code)
		}
	}
}

func TestEvents(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodGet, "/api/v1/namespaces/team-a-dev/events", func(w http.ResponseWriter, r *http.Request) {
		sel := r.URL.Query().Get("fieldSelector")
		ev := func(name, kind, reason string, ts int64) corev1.Event {
			return corev1.Event{
				ObjectMeta:     metav1.ObjectMeta{Name: name + reason, Namespace: "team-a-dev"},
				InvolvedObject: corev1.ObjectReference{Kind: kind, Name: name, Namespace: "team-a-dev"},
				Reason:         reason,
				LastTimestamp:  metav1.Unix(ts, 0),
			}
		}
		var items []corev1.Event
		switch sel {
		case "involvedObject.name=web":
			// The launcher pod shares the VM's name; its events are not the VM's.
			items = []corev1.Event{ev("web", "VirtualMachine", "Started", 100), ev("web", "Pod", "Pulled", 200), ev("web", "VirtualMachineInstance", "Migrated", 300)}
		case "involvedObject.kind=VirtualMachine":
			items = []corev1.Event{ev("web", "VirtualMachine", "Started", 100)}
		case "involvedObject.kind=VirtualMachineInstance":
			items = []corev1.Event{ev("db", "VirtualMachineInstance", "Created", 400)}
		}
		writeObj(w, corev1.EventList{TypeMeta: metav1.TypeMeta{Kind: "EventList", APIVersion: "v1"}, Items: items})
	})
	s := fc.server(nil, Config{})

	rec := do(s, adminToken, http.MethodGet, vmPath+"/events", "")
	wantStatus(t, rec, http.StatusOK)
	got := decodeBody[[]model.Event](t, rec)
	if len(got) != 2 || got[0].Reason != "Migrated" || got[1].Reason != "Started" {
		t.Errorf("VM events = %+v, want Migrated then Started, no Pod event", got)
	}

	rec = do(s, adminToken, http.MethodGet, "/api/events", "")
	wantStatus(t, rec, http.StatusOK)
	got = decodeBody[[]model.Event](t, rec)
	if len(got) != 2 || got[0].Name != "db" {
		t.Errorf("all events = %+v, want db first", got)
	}
	// The repoless project's namespace is not part of the managed inventory.
	if fc.called(http.MethodGet, "/api/v1/namespaces/orphans-ns/events") {
		t.Error("events listed in a repoless project's namespace")
	}

	rec = do(s, adminToken, http.MethodGet, "/api/events?namespace=orphans-ns", "")
	wantStatus(t, rec, http.StatusOK)
	if got := decodeBody[[]model.Event](t, rec); len(got) != 0 {
		t.Errorf("repoless namespace events = %+v, want none", got)
	}
}

func TestEventsClusterForbidden(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodGet, "/api/v1/namespaces/team-a-dev/events", func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
	})
	s := fc.server(nil, Config{})
	wantStatus(t, do(s, adminToken, http.MethodGet, vmPath+"/events", ""), http.StatusForbidden)
}

func TestScreenshot(t *testing.T) {
	fc := newFakeCluster(t)
	png := "\x89PNG\r\n\x1a\nframe"
	fc.on(http.MethodGet, "/apis/subresources.kubevirt.io/v1/namespaces/team-a-dev/virtualmachineinstances/web/vnc/screenshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(png))
	})
	s := fc.server(nil, Config{})

	rec := do(s, adminToken, http.MethodGet, vmPath+"/screenshot", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.String() != png {
		t.Errorf("body = %q, want the PNG bytes", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v, want image/png and no-store", rec.Header())
	}

	// A stopped VM has no VMI: the subresource 404s and so does the route.
	rec = do(s, adminToken, http.MethodGet, "/api/vms/team-a-dev/stopped/screenshot", "")
	wantStatus(t, rec, http.StatusNotFound)
}

func TestRuntimeOps(t *testing.T) {
	base := "/apis/subresources.kubevirt.io/v1/namespaces/team-a-dev/"
	cases := []struct {
		route, verb, path string
	}{
		{"restart", "Restart", base + "virtualmachines/web/restart"},
		{"migrate", "Live-migration", base + "virtualmachines/web/migrate"},
		{"pause", "Pause", base + "virtualmachineinstances/web/pause"},
		{"unpause", "Unpause", base + "virtualmachineinstances/web/unpause"},
	}
	for _, c := range cases {
		t.Run(c.route, func(t *testing.T) {
			fc := newFakeCluster(t)
			fc.on(http.MethodPut, c.path, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) })
			s := fc.server(nil, Config{})
			wantStatus(t, do(s, adminToken, http.MethodPost, vmPath+"/"+c.route, ""), http.StatusNoContent)
			if !fc.called(http.MethodPut, c.path) {
				t.Errorf("subresource %s not called; saw %v", c.path, fc.seen())
			}
		})
	}
}

// A pinned migration carries the target host as an added node selector;
// a VMI state conflict is the caller's 409, not a 500.
func TestMigrateTargetAndConflict(t *testing.T) {
	fc := newFakeCluster(t)
	var body string
	path := "/apis/subresources.kubevirt.io/v1/namespaces/team-a-dev/virtualmachines/web/migrate"
	fc.on(http.MethodPut, path, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusAccepted)
	})
	fc.on(http.MethodPut, "/apis/subresources.kubevirt.io/v1/namespaces/team-a-dev/virtualmachineinstances/web/pause", func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusConflict, metav1.StatusReasonConflict)
	})
	s := fc.server(nil, Config{})

	wantStatus(t, do(s, adminToken, http.MethodPost, vmPath+"/migrate", `{"node":"worker-2"}`), http.StatusNoContent)
	if !strings.Contains(body, `"kubernetes.io/hostname":"worker-2"`) {
		t.Errorf("migrate body = %q, want the hostname selector", body)
	}
	wantStatus(t, do(s, adminToken, http.MethodPost, vmPath+"/migrate", `{"node":`), http.StatusBadRequest)
	wantStatus(t, do(s, adminToken, http.MethodPost, vmPath+"/pause", ""), http.StatusConflict)
}

func TestSnapshots(t *testing.T) {
	fc := newFakeCluster(t)
	snapPath := "/apis/snapshot.kubevirt.io/v1beta1/namespaces/team-a-dev/virtualmachinesnapshots"
	snap := func(name, src, created string) map[string]any {
		return map[string]any{
			"apiVersion": "snapshot.kubevirt.io/v1beta1", "kind": "VirtualMachineSnapshot",
			"metadata": map[string]any{"name": name, "namespace": "team-a-dev", "creationTimestamp": created},
			"spec":     map[string]any{"source": map[string]any{"kind": "VirtualMachine", "name": src}},
			"status":   map[string]any{"phase": "Succeeded", "readyToUse": true},
		}
	}
	fc.on(http.MethodGet, snapPath, unstructuredList("snapshot.kubevirt.io/v1beta1", "VirtualMachineSnapshot",
		snap("old", "web", "2026-01-01T00:00:00Z"), snap("other", "db", "2026-01-03T00:00:00Z"), snap("new", "web", "2026-01-02T00:00:00Z")))
	var created, restore map[string]any
	fc.on(http.MethodPost, snapPath, echoCreate(&created, nil))
	fc.on(http.MethodPost, "/apis/snapshot.kubevirt.io/v1beta1/namespaces/team-a-dev/virtualmachinerestores", echoCreate(&restore, nil))
	fc.on(http.MethodDelete, snapPath+"/old", func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess})
	})
	s := fc.server(nil, Config{})

	rec := do(s, adminToken, http.MethodGet, vmPath+"/snapshots", "")
	wantStatus(t, rec, http.StatusOK)
	got := decodeBody[[]model.Snapshot](t, rec)
	if len(got) != 2 || got[0].Name != "new" || got[1].Name != "old" || !got[0].ReadyToUse {
		t.Errorf("snapshots = %+v, want web's two, newest first", got)
	}

	rec = do(s, adminToken, http.MethodPost, vmPath+"/snapshots", `{"name":"before-upgrade"}`)
	wantStatus(t, rec, http.StatusOK)
	if decodeBody[map[string]string](t, rec)["name"] != "before-upgrade" || created["metadata"].(map[string]any)["name"] != "before-upgrade" {
		t.Errorf("named snapshot: response %q, created %v", rec.Body.String(), created["metadata"])
	}

	rec = do(s, adminToken, http.MethodPost, vmPath+"/snapshots", "")
	wantStatus(t, rec, http.StatusOK)
	if name := decodeBody[map[string]string](t, rec)["name"]; !strings.HasPrefix(name, "web-") {
		t.Errorf("derived snapshot name = %q, want web-<timestamp>", name)
	}

	wantStatus(t, do(s, adminToken, http.MethodPost, vmPath+"/snapshots/new/restore", ""), http.StatusNoContent)
	if restore["spec"].(map[string]any)["virtualMachineSnapshotName"] != "new" {
		t.Errorf("restore spec = %v", restore["spec"])
	}

	wantStatus(t, do(s, adminToken, http.MethodDelete, vmPath+"/snapshots/old", ""), http.StatusNoContent)
	wantStatus(t, do(s, adminToken, http.MethodDelete, vmPath+"/snapshots/ghost", ""), http.StatusNotFound)
}

func TestSnapshotCreateForbidden(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodPost, "/apis/snapshot.kubevirt.io/v1beta1/namespaces/team-a-dev/virtualmachinesnapshots", func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
	})
	s := fc.server(nil, Config{})
	wantStatus(t, do(s, adminToken, http.MethodPost, vmPath+"/snapshots", `{"name":"x"}`), http.StatusForbidden)
}

func TestClones(t *testing.T) {
	fc := newFakeCluster(t)
	clonePath := "/apis/clone.kubevirt.io/v1beta1/namespaces/team-a-dev/virtualmachineclones"
	clone := func(name, kind, src, target string) map[string]any {
		return map[string]any{
			"apiVersion": "clone.kubevirt.io/v1beta1", "kind": "VirtualMachineClone",
			"metadata": map[string]any{"name": name, "namespace": "team-a-dev", "creationTimestamp": "2026-01-01T00:00:00Z"},
			"spec": map[string]any{
				"source": map[string]any{"kind": kind, "name": src},
				"target": map[string]any{"kind": "VirtualMachine", "name": target},
			},
			"status": map[string]any{"phase": "Succeeded"},
		}
	}
	fc.on(http.MethodGet, clonePath, unstructuredList("clone.kubevirt.io/v1beta1", "VirtualMachineClone",
		clone("c1", "VirtualMachine", "web", "web-copy"),
		clone("c2", "VirtualMachineSnapshot", "web", "from-snap"),
		clone("c3", "VirtualMachine", "db", "db-copy")))
	var created map[string]any
	fc.on(http.MethodPost, clonePath, echoCreate(&created, nil))
	s := fc.server(nil, Config{})

	rec := do(s, adminToken, http.MethodGet, vmPath+"/clones", "")
	wantStatus(t, rec, http.StatusOK)
	got := decodeBody[[]model.Clone](t, rec)
	if len(got) != 1 || got[0].Name != "c1" || got[0].Target != "web-copy" || got[0].Phase != "Succeeded" {
		t.Errorf("clones = %+v, want only c1", got)
	}

	for body, want := range map[string]int{
		``:                    http.StatusBadRequest,
		`{"target":"  "}`:     http.StatusBadRequest,
		`{"target":"web"}`:    http.StatusBadRequest,
		`{"target":`:          http.StatusBadRequest,
		`{"target":"web-2 "}`: http.StatusOK,
	} {
		rec := do(s, adminToken, http.MethodPost, vmPath+"/clone", body)
		if rec.Code != want {
			t.Errorf("clone %q = %d %q, want %d", body, rec.Code, rec.Body.String(), want)
		}
	}
	spec := created["spec"].(map[string]any)
	if spec["target"].(map[string]any)["name"] != "web-2" || spec["source"].(map[string]any)["name"] != "web" {
		t.Errorf("clone spec = %v, want web -> web-2 (trimmed)", spec)
	}
	if name := created["metadata"].(map[string]any)["name"].(string); !strings.HasPrefix(name, "clone-web-2-") {
		t.Errorf("clone CR name = %q", name)
	}
}

func TestPermissions(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssrr = func(token, ns string) []authzv1.ResourceRule {
		return []authzv1.ResourceRule{
			{Verbs: []string{"list"}, APIGroups: []string{"kubevirt.io"}, Resources: []string{"virtualmachines"}},
			{Verbs: []string{"*"}, APIGroups: []string{"snapshot.kubevirt.io"}, Resources: []string{"*"}},
		}
	}
	s := fc.server(nil, Config{})

	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/permissions", ""), http.StatusBadRequest)
	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/permissions?namespace=elsewhere", ""), http.StatusNotFound)

	rec := do(s, adminToken, http.MethodGet, "/api/permissions?namespace=team-a-dev", "")
	wantStatus(t, rec, http.StatusOK)
	p := decodeBody[model.Permissions](t, rec)
	allowed := map[string]bool{}
	for _, c := range p.Capabilities {
		allowed[c.ID] = c.Allowed
	}
	if p.Namespace != "team-a-dev" || !allowed["view"] || !allowed["snapshot"] || !allowed["restore"] || allowed["restart"] || allowed["clone"] {
		t.Errorf("permissions = %+v", p)
	}
}

// A tenant who reads VMs only in its own namespace (the SSRR fallback) sees
// that project and nothing else.
func TestTenantVisibilityThroughRulesReview(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssrr = func(token, ns string) []authzv1.ResourceRule {
		if token == tenantToken && ns == "team-a-dev" {
			return []authzv1.ResourceRule{{Verbs: []string{"get", "list"}, APIGroups: []string{"kubevirt.io"}, Resources: []string{"virtualmachines"}}}
		}
		return nil
	}
	fc.on(http.MethodGet, "/apis/clone.kubevirt.io/v1beta1/namespaces/team-a-dev/virtualmachineclones", unstructuredList("clone.kubevirt.io/v1beta1", "VirtualMachineClone"))
	s := fc.server(nil, Config{})

	rec := do(s, tenantToken, http.MethodGet, vmPath+"/clones", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.String() != "[]\n" {
		t.Errorf("clones = %q, want an empty list", rec.Body.String())
	}
	wantStatus(t, do(s, tenantToken, http.MethodGet, "/api/vms/orphans-ns/x/clones", ""), http.StatusNotFound)
}
