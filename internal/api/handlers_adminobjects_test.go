package api

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/config"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

var installRef = config.InstallRef{Namespace: "dotvirt", Name: "x"}

// adminObjectsServer serves an install whose status lists the operator guard
// and one object no install may ask for. The install and the guard answer
// admin-token alone; applies are recorded with their bearer.
func adminObjectsServer(t *testing.T) (*Server, *fakeCluster, func() map[string]string) {
	t.Helper()
	fc := newFakeCluster(t)
	guard := adminobjects.Guard(adminobjects.GuardParams{
		Operator: "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller", Namespace: "dotvirt", ArgoNamespace: "argocd",
	})
	var entries []any
	for _, o := range guard {
		entries = append(entries, map[string]any{"purpose": o.Purpose, "state": "NotInstalled", "object": o.Object.Object})
	}
	entries = append(entries, map[string]any{"purpose": "rogue", "state": "NotInstalled", "object": map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": "rogue"},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "cluster-admin"},
		"subjects": []any{map[string]any{"kind": "User", "name": "mallory"}},
	}})
	fc.on(http.MethodGet, "/apis/dotvirt.io/v1alpha1/namespaces/dotvirt/dotvirts/x", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != adminToken {
			writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
			return
		}
		writeObj(w, map[string]any{
			"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt", "metadata": map[string]any{"name": "x", "namespace": "dotvirt"},
			"status": map[string]any{"adminObjects": entries},
		})
	})
	var mu sync.Mutex
	stored := map[string]map[string]any{}
	applied := map[string]string{}
	for _, o := range guard {
		ref, _ := adminobjects.Allow(adminobjects.Allowed("dotvirt"), o.Object)
		path := "/apis/" + ref.Group + "/" + ref.Version + "/" + ref.Resource + "/" + ref.Name
		fc.on(http.MethodGet, path, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			obj := stored[path]
			mu.Unlock()
			if bearer(r) != adminToken {
				writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
				return
			}
			if obj == nil {
				writeStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound)
				return
			}
			writeObj(w, obj)
		})
		fc.on(http.MethodPatch, path, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var obj map[string]any
			if err := json.Unmarshal(body, &obj); err != nil {
				t.Errorf("apply body: %v", err)
			}
			mu.Lock()
			applied[ref.Kind+"/"+ref.Name] = bearer(r)
			if bearer(r) == adminToken {
				stored[path] = obj
			}
			mu.Unlock()
			if bearer(r) != adminToken {
				writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
				return
			}
			writeObj(w, obj)
		})
	}
	fc.on(http.MethodPatch, "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/rogue", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		applied["ClusterRoleBinding/rogue"] = bearer(r)
		mu.Unlock()
		writeObj(w, map[string]any{})
	})
	s := fc.server(nil, Config{Install: installRef})
	return s, fc, func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]string{}
		for k, v := range applied {
			out[k] = v
		}
		return out
	}
}

// Installing the install's admin objects runs under the caller's token,
// behind the review the console asks before offering it, and applies only
// what an install may ask for.
func TestAdminObjectsInstallAsTheCaller(t *testing.T) {
	s, fc, applied := adminObjectsServer(t)
	caps := func(token string) bool {
		c := decodeBody[model.Capabilities](t, do(s, token, http.MethodGet, "/api/capabilities", "")).Cluster
		return c != nil && c.AdminObjects
	}
	if !caps(adminToken) || caps(tenantToken) {
		t.Errorf("adminObjects: admin %v, tenant %v; want true, false", caps(adminToken), caps(tenantToken))
	}
	if fc.called(http.MethodGet, "/apis/dotvirt.io/v1alpha1/namespaces/dotvirt/dotvirts/x") {
		t.Error("the capability read the install; it must ask reviews only")
	}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		wantStatus(t, do(s, tenantToken, method, "/api/admin-objects", ""), http.StatusForbidden)
	}
	if got := applied(); len(got) != 0 || fc.called(http.MethodGet, "/apis/dotvirt.io/v1alpha1/namespaces/dotvirt/dotvirts/x") {
		t.Fatalf("a refused caller reached the cluster: applied %v", got)
	}

	rec := do(s, adminToken, http.MethodGet, "/api/admin-objects", "")
	wantStatus(t, rec, http.StatusOK)
	listed := decodeBody[model.AdminObjects](t, rec)
	if len(listed.Objects) != 4 {
		t.Fatalf("listed %d objects, want the guard's four and not the rogue one: %+v", len(listed.Objects), listed.Objects)
	}
	for _, o := range listed.Objects {
		if o.State != string(adminobjects.NotInstalled) || o.Purpose == "" || o.Object["kind"] != o.Kind {
			t.Errorf("before install: %+v", o)
		}
	}

	rec = do(s, adminToken, http.MethodPost, "/api/admin-objects", "")
	wantStatus(t, rec, http.StatusOK)
	installed := decodeBody[model.AdminObjects](t, rec)
	for _, o := range installed.Objects {
		if o.State != string(adminobjects.Installed) {
			t.Errorf("after install: %s %s is %s (%s)", o.Kind, o.Name, o.State, o.Detail)
		}
	}
	got := applied()
	if _, ok := got["ClusterRoleBinding/rogue"]; ok {
		t.Error("the console applied an object no install may ask for")
	}
	if len(got) != 4 {
		t.Errorf("applied %v, want the guard's four", got)
	}
	for what, b := range got {
		if b != adminToken {
			t.Errorf("%s applied with %q, want the caller's token", what, b)
		}
	}
}
