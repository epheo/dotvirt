package api

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/epheo/dotvirt/internal/config"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

var (
	// The install env the operator writes, steered at a tenant's Dotvirt: the
	// console reads its own namespace's instead.
	installRef = config.InstallRef{Namespace: "team-a", Name: "decoy"}
	// The pins the console must render from its reads: the operator's
	// Deployment's account, the install's spec, the default ArgoCD target off
	// OpenShift, and the images an admin chose.
	adminWant = adminobjects.Params{
		Operator: "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller", Namespace: "dotvirt",
		ArgoNamespace: "argocd", ArgoController: "argocd-application-controller",
		SpecImage:    "registry.example/team/dotvirt:custom",
		DotvirtImage: "quay.io/epheo/dotvirt@sha256:aaaa", ForgejoImage: "codeberg.org/forgejo/forgejo:12-rootless",
		ConsoleHost: "console.example",

		BrandingConfigMap: "brand", ForgeSecret: "creds",
		OIDCSecret: "admin-oidc", OIDCSecretKey: "client-secret", OIDCIssuer: "https://idp.example",
		InstallName: "x", InstallUID: "uid-x",
	}
)

const (
	installPath         = "/apis/dotvirt.io/v1alpha1/namespaces/dotvirt/dotvirts"
	operatorPath        = "/apis/apps/v1/deployments"
	operatorInstallPath = "/apis/dotvirt.io/v1alpha1/namespaces/dotvirt-operator/dotvirts"
	consoleBindingPath  = "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/dotvirt"
)

// consoleBinding is ClusterRoleBinding dotvirt as the operator writes it for
// the install in namespace.
func consoleBinding(namespace string) rbacv1.ClusterRoleBinding {
	return rbacv1.ClusterRoleBinding{
		TypeMeta:   metav1.TypeMeta{Kind: "ClusterRoleBinding", APIVersion: "rbac.authorization.k8s.io/v1"},
		ObjectMeta: metav1.ObjectMeta{Name: "dotvirt", CreationTimestamp: metav1.Now()},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "dotvirt"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "dotvirt", Namespace: namespace}},
	}
}

func dotvirtList(items ...any) map[string]any {
	return map[string]any{"apiVersion": "dotvirt.io/v1alpha1", "kind": "DotvirtList", "metadata": map[string]any{}, "items": append([]any{}, items...)}
}

// adminObjectsServer serves an install whose status an operator tampered
// with (a deny-everything policy under the guard's name, and a binding no
// install may ask for), the operator's Deployment, and the guard's objects;
// all answer admin-token alone. Applies are recorded with bearer and body.
func adminObjectsServer(t *testing.T, seed ...*unstructured.Unstructured) (*Server, *fakeCluster, func() map[string]applied) {
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
		writeObj(w, map[string]any{"apiVersion": "dotvirt.io/v1alpha1", "kind": "DotvirtList", "metadata": map[string]any{}, "items": []any{map[string]any{
			"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt", "metadata": map[string]any{"name": "x", "namespace": "dotvirt", "uid": "uid-x"},
			"spec": map[string]any{"image": adminWant.SpecImage, "ingress": map[string]any{"host": adminWant.ConsoleHost},
				"branding": map[string]any{"configMap": adminWant.BrandingConfigMap}, "forge": map[string]any{"credentialsSecret": adminWant.ForgeSecret},
				"auth": map[string]any{"oidc": map[string]any{"issuerURL": adminWant.OIDCIssuer,
					"clientSecret": map[string]any{"name": adminWant.OIDCSecret, "key": adminWant.OIDCSecretKey}}}},
			"status": map[string]any{"adminObjects": []any{
				map[string]any{"purpose": "tampered", "state": "NotInstalled", "kind": "ValidatingAdmissionPolicy", "name": adminobjects.GuardSecretsName,
					"object": map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicy",
						"metadata": map[string]any{"name": adminobjects.GuardSecretsName},
						"spec":     map[string]any{"validations": []any{map[string]any{"expression": "false"}}}}},
				// The forge credential's grant, widened to every Secret.
				map[string]any{"purpose": "tampered", "state": "NotInstalled", "kind": "Role", "name": adminobjects.CredentialGrantName, "namespace": "dotvirt",
					"object": map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role",
						"metadata": map[string]any{"name": adminobjects.CredentialGrantName, "namespace": "dotvirt"},
						"rules":    []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "verbs": []any{"*"}}}}},
				map[string]any{"purpose": "rogue", "state": "NotInstalled", "object": map[string]any{
					"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": "rogue"},
					"roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "cluster-admin"}}},
			}},
		}}})
	})
	fc.on(http.MethodGet, operatorPath, func(w http.ResponseWriter, r *http.Request) {
		if !adminOnly(w, r) {
			return
		}
		if r.URL.Query().Get("fieldSelector") != "metadata.name="+adminobjects.OperatorDeployment {
			t.Errorf("operator Deployment listed with %q", r.URL.RawQuery)
		}
		d := appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: adminobjects.OperatorDeployment, Namespace: "dotvirt-operator"}}
		d.Spec.Template.Spec.ServiceAccountName = "dotvirt-operator-controller"
		d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "manager", Env: []corev1.EnvVar{
			{Name: "RELATED_IMAGE_DOTVIRT", Value: adminWant.DotvirtImage},
			{Name: "RELATED_IMAGE_FORGEJO", Value: adminWant.ForgejoImage},
		}}}
		// A tenant's lookalike, whose account holds no right on Dotvirt status.
		decoy := appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: adminobjects.OperatorDeployment, Namespace: "team-a"}}
		decoy.Spec.Template.Spec.Containers = []corev1.Container{{Name: "manager", Env: []corev1.EnvVar{
			{Name: "RELATED_IMAGE_DOTVIRT", Value: "registry.example/evil:1"},
		}}}
		writeObj(w, appsv1.DeploymentList{TypeMeta: metav1.TypeMeta{Kind: "DeploymentList", APIVersion: "apps/v1"}, Items: []appsv1.Deployment{decoy, d}})
	})
	fc.on(http.MethodGet, operatorInstallPath, func(w http.ResponseWriter, r *http.Request) {
		if adminOnly(w, r) {
			writeObj(w, dotvirtList())
		}
	})
	fc.on(http.MethodGet, consoleBindingPath, func(w http.ResponseWriter, r *http.Request) {
		if adminOnly(w, r) {
			writeObj(w, consoleBinding("dotvirt"))
		}
	})
	fc.on(http.MethodPost, "/apis/authorization.k8s.io/v1/subjectaccessreviews", func(w http.ResponseWriter, r *http.Request) {
		if !adminOnly(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		obj, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, &authzv1.SubjectAccessReview{})
		if err != nil {
			t.Errorf("review body: %v", err)
			return
		}
		review := obj.(*authzv1.SubjectAccessReview)
		a := review.Spec.ResourceAttributes
		review.Status.Allowed = review.Spec.User == adminWant.Operator && a != nil && a.Verb == "patch" &&
			a.Resource == "dotvirts" && a.Subresource == "status" && a.Namespace == "dotvirt"
		writeObj(w, review)
	})
	var mu sync.Mutex
	stored := map[string]map[string]any{}
	seen := map[string]applied{}
	allowed := adminobjects.Allowed("dotvirt")
	pathOf := func(ref adminobjects.Ref) string {
		if ref.Namespace != "" {
			return "/apis/" + ref.Group + "/" + ref.Version + "/namespaces/" + ref.Namespace + "/" + ref.Resource + "/" + ref.Name
		}
		return "/apis/" + ref.Group + "/" + ref.Version + "/" + ref.Resource + "/" + ref.Name
	}
	for _, o := range seed {
		ref, ok := adminobjects.Allow(allowed, o)
		if !ok {
			t.Fatalf("seeded %s %s, which no install may ask for", o.GetKind(), o.GetName())
		}
		stored[pathOf(ref)] = o.Object
	}
	for _, ref := range allowed {
		path := pathOf(ref)
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
	s := fc.server(nil, Config{Install: installRef, OwnNamespace: "dotvirt"})
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

// The console installs its own render of the admin objects, every pin read
// under the caller's token (the install's spec, the operator's Deployment),
// behind the review it asks before offering it. What the operator wrote,
// status or env, changes nothing.
func TestAdminObjectsInstallTheConsolesRender(t *testing.T) {
	// Env the operator writes on the console's Deployment, steered at another
	// account: the render must not follow it.
	t.Setenv("DOTVIRT_OPERATOR_USERNAME", "system:serviceaccount:dotvirt-operator:default")
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

	want := adminWant
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

// The guard pins the install the operator acts on, as it resolves it: a
// console that install moved away from refuses to pin its own namespace.
func TestAdminObjectsPinOnlyTheOperatorsInstall(t *testing.T) {
	operator := func(env ...corev1.EnvVar) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			d := appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: adminobjects.OperatorDeployment, Namespace: "dotvirt-operator"}}
			d.Spec.Template.Spec.ServiceAccountName = "dotvirt-operator-controller"
			d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "manager", Env: env}}
			writeObj(w, appsv1.DeploymentList{TypeMeta: metav1.TypeMeta{Kind: "DeploymentList", APIVersion: "apps/v1"}, Items: []appsv1.Deployment{d}})
		}
	}
	for name, steer := range map[string]func(*fakeCluster){
		"an admin names another namespace on the operator": func(fc *fakeCluster) {
			fc.on(http.MethodGet, operatorPath, operator(corev1.EnvVar{Name: adminobjects.InstallNamespaceEnv, Value: "elsewhere"}))
		},
		"a Dotvirt in the operator's own namespace": func(fc *fakeCluster) {
			fc.on(http.MethodGet, operatorInstallPath, func(w http.ResponseWriter, r *http.Request) {
				writeObj(w, dotvirtList(map[string]any{"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt",
					"metadata": map[string]any{"name": "x", "namespace": "dotvirt-operator"}}))
			})
		},
		"the console's binding in another namespace": func(fc *fakeCluster) {
			fc.on(http.MethodGet, consoleBindingPath, func(w http.ResponseWriter, r *http.Request) {
				writeObj(w, consoleBinding("elsewhere"))
			})
			fc.on(http.MethodGet, "/apis/dotvirt.io/v1alpha1/namespaces/elsewhere/dotvirts", func(w http.ResponseWriter, r *http.Request) {
				writeObj(w, dotvirtList(map[string]any{"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt",
					"metadata": map[string]any{"name": "x", "namespace": "elsewhere"}}))
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, fc, seen := adminObjectsServer(t)
			steer(fc)
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				wantStatus(t, do(s, adminToken, method, "/api/admin-objects", ""), http.StatusConflict)
			}
			if got := seen(); len(got) != 0 {
				t.Errorf("applied %v", got)
			}
		})
	}
	s, fc, _ := adminObjectsServer(t)
	fc.on(http.MethodGet, operatorPath, operator(corev1.EnvVar{Name: adminobjects.InstallNamespaceEnv, Value: "dotvirt"}))
	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/admin-objects", ""), http.StatusOK)
}

// Where routes are served but no cluster ingress domain is (MicroShift), the
// console's host is the one routers report for its Route, read as the admin.
func TestAdminObjectsPinTheRoutersHost(t *testing.T) {
	s, fc, seen := adminObjectsServer(t)
	fc.on(http.MethodGet, installPath, func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, dotvirtList(map[string]any{"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt",
			"metadata": map[string]any{"name": "x", "namespace": "dotvirt"}, "spec": map[string]any{"image": adminWant.SpecImage}}))
	})
	fc.on(http.MethodGet, "/apis/route.openshift.io/v1", func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: "route.openshift.io/v1",
			APIResources: []metav1.APIResource{{Name: "routes", Kind: "Route", Namespaced: true}}})
	})
	fc.on(http.MethodGet, "/apis/route.openshift.io/v1/namespaces/dotvirt/routes/dotvirt", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != adminToken {
			writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
			return
		}
		writeObj(w, map[string]any{"apiVersion": "route.openshift.io/v1", "kind": "Route", "metadata": map[string]any{"name": "dotvirt", "namespace": "dotvirt"},
			"status": map[string]any{"ingress": []any{map[string]any{"host": "dotvirt-dotvirt.apps.micro"}}}})
	})
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/admin-objects", ""), http.StatusOK)
	body, _ := json.Marshal(seen()["ValidatingAdmissionPolicy/"+adminobjects.GuardRoutesName].body)
	if !strings.Contains(string(body), `\"dotvirt-dotvirt.apps.micro\"`) {
		t.Errorf("the Route rules do not pin the router's host:\n%s", body)
	}
}

// A Gateway serving the console with no host pins its listener, never a host:
// not even one routers still report for a Route left from another exposure.
func TestAdminObjectsPinTheHostlessConsolesListener(t *testing.T) {
	s, fc, seen := adminObjectsServer(t)
	fc.on(http.MethodGet, installPath, func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, dotvirtList(map[string]any{"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt",
			"metadata": map[string]any{"name": "x", "namespace": "dotvirt"}, "spec": map[string]any{"image": adminWant.SpecImage,
				"ingress": map[string]any{"gateway": map[string]any{"name": "edge", "sectionName": "https"}}}}))
	})
	fc.on(http.MethodGet, "/apis/route.openshift.io/v1", func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: "route.openshift.io/v1",
			APIResources: []metav1.APIResource{{Name: "routes", Kind: "Route", Namespaced: true}}})
	})
	fc.on(http.MethodGet, "/apis/route.openshift.io/v1/namespaces/dotvirt/routes/dotvirt", func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, map[string]any{"apiVersion": "route.openshift.io/v1", "kind": "Route", "metadata": map[string]any{"name": "dotvirt", "namespace": "dotvirt"},
			"status": map[string]any{"ingress": []any{map[string]any{"host": "dotvirt-dotvirt.apps.micro"}}}})
	})
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/admin-objects", ""), http.StatusOK)
	routes, _ := json.Marshal(seen()["ValidatingAdmissionPolicy/"+adminobjects.GuardRoutesName].body)
	if strings.Contains(string(routes), "apps.micro") {
		t.Errorf("a leftover Route's host is pinned:\n%s", routes)
	}
	httproutes, _ := json.Marshal(seen()["ValidatingAdmissionPolicy/"+adminobjects.GuardHTTPRoutesName].body)
	if !strings.Contains(string(httproutes), `r.name == \"edge\"`) || !strings.Contains(string(httproutes), `r.sectionName : \"\") == \"https\"`) {
		t.Errorf("the HTTPRoute rules do not pin the listener:\n%s", httproutes)
	}
}

// The forge credential's grant is the console's render from the install's
// spec.forge, read as the admin: listed for an admin's Secret, with write verbs
// for a managed forge, and never for the default credential or a Secret the
// operator generates for itself. A grant planted in status changes nothing.
func TestAdminObjectsGrantTheCredentialFromTheSpec(t *testing.T) {
	for _, c := range []struct {
		forge map[string]any
		verbs []string
	}{
		{map[string]any{}, nil},
		{map[string]any{"credentialsSecret": adminobjects.DefaultForgeSecret, "managed": true}, nil},
		{map[string]any{"credentialsSecret": adminobjects.SessionSecret, "managed": true}, nil},
		{map[string]any{"credentialsSecret": "creds"}, []string{"get"}},
		{map[string]any{"credentialsSecret": "creds", "managed": true}, []string{"get", "create", "patch"}},
	} {
		s, fc, _ := adminObjectsServer(t)
		fc.on(http.MethodGet, installPath, func(w http.ResponseWriter, r *http.Request) {
			writeObj(w, dotvirtList(map[string]any{"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt",
				"metadata": map[string]any{"name": "x", "namespace": "dotvirt", "uid": "uid-x"},
				"spec":     map[string]any{"image": adminWant.SpecImage, "ingress": map[string]any{"host": adminWant.ConsoleHost}, "forge": c.forge},
				"status": map[string]any{"adminObjects": []any{map[string]any{"kind": "Role", "name": adminobjects.CredentialGrantName, "namespace": "dotvirt",
					"object": map[string]any{"rules": []any{map[string]any{"verbs": []any{"*"}}}}}}},
			}))
		})
		rec := do(s, adminToken, http.MethodGet, "/api/admin-objects", "")
		wantStatus(t, rec, http.StatusOK)
		var role map[string]any
		for _, o := range decodeBody[model.AdminObjects](t, rec).Objects {
			if o.Kind == "Role" && o.Name == adminobjects.CredentialGrantName {
				role = o.Object
			}
		}
		if c.verbs == nil {
			if role != nil {
				t.Errorf("forge %v: the grant is offered", c.forge)
			}
			continue
		}
		rules, _, _ := unstructured.NestedSlice(role, "rules")
		if len(rules) != 1 {
			t.Fatalf("forge %v: Role rules %v", c.forge, rules)
		}
		verbs, _, _ := unstructured.NestedStringSlice(rules[0].(map[string]any), "verbs")
		names, _, _ := unstructured.NestedStringSlice(rules[0].(map[string]any), "resourceNames")
		if !slices.Equal(verbs, c.verbs) || !slices.Equal(names, []string{"creds"}) {
			t.Errorf("forge %v: the grant gives %v on %v, want %v on creds", c.forge, verbs, names, c.verbs)
		}
	}
}

// Installing the grant takes the caller's own right to create and patch it:
// an admin who may write everything but Roles and RoleBindings in the install
// namespace is not offered the install, and the handler reads nothing.
func TestAdminObjectsCapabilityCoversTheCredentialGrant(t *testing.T) {
	s, fc, seen := adminObjectsServer(t)
	// The SA's startup reviews may still be in flight: swap the fake's verdicts under its lock.
	fc.mu.Lock()
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		grant := a.Group == "rbac.authorization.k8s.io" && slices.Contains([]string{"roles", "rolebindings"}, a.Resource)
		return token == adminToken && !grant
	}
	fc.mu.Unlock()
	c := decodeBody[model.Capabilities](t, do(s, adminToken, http.MethodGet, "/api/capabilities", "")).Cluster
	if c != nil && c.AdminObjects {
		t.Error("offered without the right to install the forge credential's grant")
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		wantStatus(t, do(s, adminToken, method, "/api/admin-objects", ""), http.StatusForbidden)
	}
	if got := seen(); len(got) != 0 || fc.called(http.MethodGet, installPath) {
		t.Errorf("a caller without the grant's rights reached the cluster: applied %v", got)
	}
}

// handMadeGrant is the forge credential's grant as an admin made it with
// kubectl: no owner, no labels.
func handMadeGrant(t *testing.T, p adminobjects.Params) []*unstructured.Unstructured {
	t.Helper()
	role, binding := adminobjects.CredentialGrant(p)
	var out []*unstructured.Unstructured
	for _, o := range []runtime.Object{role, binding} {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetOwnerReferences(nil)
		u.SetLabels(nil)
		unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
		out = append(out, u)
	}
	return out
}

// Install over a grant an admin made by hand keeps it ownerless: adding an
// owner takes delete on the object where the apiserver enforces owner
// permissions, and the admin's grant stays the admin's.
func TestAdminObjectsInstallKeepsAHandMadeGrantsOwner(t *testing.T) {
	s, _, seen := adminObjectsServer(t, handMadeGrant(t, adminWant)...)
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/admin-objects", ""), http.StatusOK)
	got := seen()
	for _, kind := range []string{"Role", "RoleBinding"} {
		a, ok := got[kind+"/"+adminobjects.CredentialGrantName]
		if !ok {
			t.Fatalf("%s not applied", kind)
		}
		if owners, found, _ := unstructured.NestedSlice(a.body, "metadata", "ownerReferences"); found && len(owners) > 0 {
			t.Errorf("%s applied with owners %v over a hand-made grant", kind, owners)
		}
	}
	// Fresh, the render's owner goes with it.
	s, _, seen = adminObjectsServer(t)
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/admin-objects", ""), http.StatusOK)
	if owners, _, _ := unstructured.NestedSlice(seen()["Role/"+adminobjects.CredentialGrantName].body, "metadata", "ownerReferences"); len(owners) != 1 {
		t.Errorf("a fresh grant applied with owners %v, want the install", owners)
	}
}

// A grant left after the credential went back to the operator's own is listed
// Stale with how to remove it, never applied.
func TestAdminObjectsListAStaleGrant(t *testing.T) {
	s, fc, seen := adminObjectsServer(t, handMadeGrant(t, adminWant)...)
	fc.on(http.MethodGet, installPath, func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, dotvirtList(map[string]any{"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt",
			"metadata": map[string]any{"name": "x", "namespace": "dotvirt", "uid": "uid-x"},
			"spec":     map[string]any{"image": adminWant.SpecImage, "ingress": map[string]any{"host": adminWant.ConsoleHost}}}))
	})
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := do(s, adminToken, method, "/api/admin-objects", "")
		wantStatus(t, rec, http.StatusOK)
		var stale []string
		for _, o := range decodeBody[model.AdminObjects](t, rec).Objects {
			if o.State == string(adminobjects.Stale) {
				stale = append(stale, o.Kind)
				if o.Object != nil || !strings.Contains(o.Detail, adminobjects.CredentialStaleCommand) {
					t.Errorf("%s: stale %s carries object %v, detail %q", method, o.Kind, o.Object, o.Detail)
				}
			}
		}
		if !slices.Equal(stale, []string{"Role", "RoleBinding"}) {
			t.Errorf("%s: stale %v, want the grant's Role and RoleBinding", method, stale)
		}
	}
	if _, ok := seen()["Role/"+adminobjects.CredentialGrantName]; ok {
		t.Error("the stale grant was applied")
	}
}

// The console's render of the grant, from its real reads (the install's spec,
// name and uid, the operator's Deployment), is the shared render the operator
// takes too (adminobjects.CredentialParams): one source.
func TestAdminObjectsRenderTheSharedGrant(t *testing.T) {
	s, fc, _ := adminObjectsServer(t)
	fc.on(http.MethodGet, installPath, func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, dotvirtList(map[string]any{"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt",
			"metadata": map[string]any{"name": "x", "namespace": "dotvirt", "uid": "uid-x"},
			"spec": map[string]any{"image": adminWant.SpecImage, "ingress": map[string]any{"host": adminWant.ConsoleHost},
				"forge": map[string]any{"credentialsSecret": "creds", "managed": true}}}))
	})
	rec := do(s, adminToken, http.MethodGet, "/api/admin-objects", "")
	wantStatus(t, rec, http.StatusOK)
	role, binding := adminobjects.CredentialGrant(adminobjects.CredentialParams("dotvirt", "x", "uid-x", "creds", true, adminWant.Operator))
	want := map[string]runtime.Object{"Role": role, "RoleBinding": binding}
	for _, o := range decodeBody[model.AdminObjects](t, rec).Objects {
		w, ok := want[o.Kind]
		if !ok || o.Name != adminobjects.CredentialGrantName {
			continue
		}
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(w)
		if err != nil {
			t.Fatal(err)
		}
		unstructured.RemoveNestedField(m, "metadata", "creationTimestamp")
		x, _ := json.Marshal(o.Object)
		y, _ := json.Marshal(m)
		if string(x) != string(y) {
			t.Errorf("%s: the console renders\n%s\nthe shared render is\n%s", o.Kind, x, y)
		}
		delete(want, o.Kind)
	}
	if len(want) != 0 {
		t.Errorf("not rendered: %v", want)
	}
}
