package api

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/config"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

var (
	installRef = config.InstallRef{Namespace: "dotvirt", Name: "x"}
	adminPins  = adminobjects.Params{
		Operator: "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller", Namespace: "dotvirt",
		ArgoNamespace: "argocd", ArgoController: "argocd-application-controller",
	}
	// What an admin chose: the install's spec.image and the operator
	// Deployment's env, read as the caller.
	adminChosen = adminobjects.Params{
		SpecImage:    "registry.example/team/dotvirt:custom",
		DotvirtImage: "quay.io/epheo/dotvirt@sha256:aaaa", ForgejoImage: "codeberg.org/forgejo/forgejo:12-rootless",
	}
)

const (
	installPath  = "/apis/dotvirt.io/v1alpha1/namespaces/dotvirt/dotvirts/x"
	operatorPath = "/apis/apps/v1/namespaces/dotvirt-operator/deployments/dotvirt-operator-controller"
)

// adminObjectsServer serves an install whose status an operator tampered
// with (a deny-everything policy under the guard's name, and a binding no
// install may ask for), the operator's Deployment, and the guard's objects;
// all answer admin-token alone. Applies are recorded with bearer and body.
func adminObjectsServer(t *testing.T) (*Server, *fakeCluster, func() map[string]applied) {
	t.Helper()
	fc := newFakeCluster(t)
	adminOnly := func(w http.ResponseWriter, r *http.Request) bool {
		if bearer(r) != adminToken {
			writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
			return false
		}
		return true
	}
	fc.on(http.MethodGet, installPath, func(w http.ResponseWriter, r *http.Request) {
		if !adminOnly(w, r) {
			return
		}
		writeObj(w, map[string]any{
			"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt", "metadata": map[string]any{"name": "x", "namespace": "dotvirt"},
			"spec": map[string]any{"image": adminChosen.SpecImage},
			"status": map[string]any{"adminObjects": []any{
				map[string]any{"purpose": "tampered", "state": "NotInstalled", "kind": "ValidatingAdmissionPolicy", "name": adminobjects.GuardSecretsName,
					"object": map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicy",
						"metadata": map[string]any{"name": adminobjects.GuardSecretsName},
						"spec":     map[string]any{"validations": []any{map[string]any{"expression": "false"}}}}},
				map[string]any{"purpose": "rogue", "state": "NotInstalled", "object": map[string]any{
					"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": "rogue"},
					"roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "cluster-admin"}}},
			}},
		})
	})
	fc.on(http.MethodGet, operatorPath, func(w http.ResponseWriter, r *http.Request) {
		if !adminOnly(w, r) {
			return
		}
		d := appsv1.Deployment{TypeMeta: metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"},
			ObjectMeta: metav1.ObjectMeta{Name: adminobjects.OperatorDeployment, Namespace: "dotvirt-operator"}}
		d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "manager", Env: []corev1.EnvVar{
			{Name: "RELATED_IMAGE_DOTVIRT", Value: adminChosen.DotvirtImage},
			{Name: "RELATED_IMAGE_FORGEJO", Value: adminChosen.ForgejoImage},
		}}}
		writeObj(w, d)
	})
	var mu sync.Mutex
	stored := map[string]map[string]any{}
	seen := map[string]applied{}
	for _, ref := range adminobjects.Allowed("dotvirt") {
		path := "/apis/" + ref.Group + "/" + ref.Version + "/" + ref.Resource + "/" + ref.Name
		fc.on(http.MethodGet, path, func(w http.ResponseWriter, r *http.Request) {
			if !adminOnly(w, r) {
				return
			}
			mu.Lock()
			obj := stored[path]
			mu.Unlock()
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
			seen[ref.Kind+"/"+ref.Name] = applied{bearer(r), obj}
			mu.Unlock()
			if !adminOnly(w, r) {
				return
			}
			mu.Lock()
			stored[path] = obj
			mu.Unlock()
			writeObj(w, obj)
		})
	}
	fc.on(http.MethodPatch, "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/rogue", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen["ClusterRoleBinding/rogue"] = applied{bearer: bearer(r)}
		mu.Unlock()
		writeObj(w, map[string]any{})
	})
	s := fc.server(nil, Config{Install: installRef, AdminPins: adminPins})
	return s, fc, func() map[string]applied {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]applied{}
		for k, v := range seen {
			out[k] = v
		}
		return out
	}
}

type applied struct {
	bearer string
	body   map[string]any
}

// The console installs its own render of the admin objects, from its pins and
// the images an admin chose, under the caller's token, behind the review it
// asks before offering it. What the operator wrote in status changes nothing.
func TestAdminObjectsInstallTheConsolesRender(t *testing.T) {
	s, fc, seen := adminObjectsServer(t)
	caps := func(token string) bool {
		c := decodeBody[model.Capabilities](t, do(s, token, http.MethodGet, "/api/capabilities", "")).Cluster
		return c != nil && c.AdminObjects
	}
	if !caps(adminToken) || caps(tenantToken) {
		t.Errorf("adminObjects: admin %v, tenant %v; want true, false", caps(adminToken), caps(tenantToken))
	}
	if fc.called(http.MethodGet, installPath) || fc.called(http.MethodGet, operatorPath) {
		t.Error("the capability read the cluster; it must ask reviews only")
	}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		wantStatus(t, do(s, tenantToken, method, "/api/admin-objects", ""), http.StatusForbidden)
	}
	if got := seen(); len(got) != 0 || fc.called(http.MethodGet, installPath) {
		t.Fatalf("a refused caller reached the cluster: applied %v", got)
	}

	want := adminPins
	want.SpecImage, want.DotvirtImage, want.ForgejoImage = adminChosen.SpecImage, adminChosen.DotvirtImage, adminChosen.ForgejoImage
	rendered := adminobjects.Render(want)

	rec := do(s, adminToken, http.MethodGet, "/api/admin-objects", "")
	wantStatus(t, rec, http.StatusOK)
	listed := decodeBody[model.AdminObjects](t, rec)
	if listed.Operator != want.Operator || listed.Namespace != "dotvirt" || listed.ArgoNamespace != "argocd" || listed.ArgoController != want.ArgoController ||
		!slices.Equal(listed.Images, []string{want.SpecImage, want.DotvirtImage, want.ForgejoImage}) {
		t.Errorf("pins = %+v", listed)
	}
	if len(listed.Objects) != len(rendered) {
		t.Fatalf("listed %d objects, want the render's %d", len(listed.Objects), len(rendered))
	}
	for _, o := range listed.Objects {
		if o.State != string(adminobjects.NotInstalled) || o.Purpose == "" || o.Detail == "" {
			t.Errorf("before install: %+v", o)
		}
	}

	rec = do(s, adminToken, http.MethodPost, "/api/admin-objects", "")
	wantStatus(t, rec, http.StatusOK)
	for _, o := range decodeBody[model.AdminObjects](t, rec).Objects {
		if o.State != string(adminobjects.Installed) {
			t.Errorf("after install: %s %s is %s (%s)", o.Kind, o.Name, o.State, o.Detail)
		}
	}
	got := seen()
	if _, ok := got["ClusterRoleBinding/rogue"]; ok {
		t.Error("the console applied an object from status")
	}
	if len(got) != len(rendered) {
		t.Errorf("applied %d objects, want the render's %d", len(got), len(rendered))
	}
	for _, o := range rendered {
		a := got[o.Object.GetKind()+"/"+o.Object.GetName()]
		x, _ := json.Marshal(a.body)
		y, _ := json.Marshal(o.Object.Object)
		if a.bearer != adminToken || string(x) != string(y) {
			t.Errorf("%s %s applied by %q as\n%s\nwant the render\n%s", o.Object.GetKind(), o.Object.GetName(), a.bearer, x, y)
		}
	}
}
