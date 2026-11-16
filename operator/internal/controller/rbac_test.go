package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

// unscopedRules are the operator's rules without resourceNames, each for the
// reason its marker gives (rbac_markers.go). Every other rule names its objects.
var unscopedRules = map[string][]string{
	"dotvirt.io/dotvirts":                       {"get", "list", "update", "watch"},
	"dotvirt.io/dotvirts/status":                {"get", "patch", "update"},
	"dotvirt.io/dotvirts/finalizers":            {"update"},
	"apps/deployments":                          {"get", "list", "watch"},
	"route.openshift.io/routes":                 {"get", "list", "watch"},
	"route.openshift.io/routes/custom-host":     {"create", "update"},
	"/namespaces":                               {"list", "watch"},
	"argoproj.io/applications":                  {"list"},
	"argoproj.io/appprojects":                   {"create", "delete", "list", "patch"},
	"authorization.k8s.io/subjectaccessreviews": {"create"},
	"rbac.authorization.k8s.io/rolebindings":    {"get"},
}

// operatorRole is the operator's own ClusterRole, as generated.
type operatorRole []rbacv1.PolicyRule

func readOperatorRole(t *testing.T) operatorRole {
	t.Helper()
	for _, r := range readClusterRoles(t, filepath.Join("..", "..", "config", "rbac", "role.yaml")) {
		if r.Name == "dotvirt-operator" {
			return r.Rules
		}
	}
	t.Fatal("no dotvirt-operator ClusterRole in role.yaml")
	return nil
}

// allows is RBAC's verdict on one request: a rule matches group, resource and
// verb, and names name or nothing. A rule naming nothing counts only where
// unscoped says so.
func (role operatorRole) allows(verb, group, resource, name string, unscoped func(gr string) bool) bool {
	for _, rule := range role {
		if !slices.Contains(rule.Verbs, verb) || !slices.Contains(rule.APIGroups, group) || !slices.Contains(rule.Resources, resource) {
			continue
		}
		if len(rule.ResourceNames) == 0 {
			if unscoped(group + "/" + resource) {
				return true
			}
			continue
		}
		if name != "" && slices.Contains(rule.ResourceNames, name) {
			return true
		}
	}
	return false
}

// Every rule without names is one the markers give a reason for, with exactly
// those verbs: a new unscoped verb must argue its case there first.
func TestUnscopedRulesAreTheReasonedOnes(t *testing.T) {
	got := map[string][]string{}
	for _, rule := range readOperatorRole(t) {
		if len(rule.ResourceNames) > 0 {
			continue
		}
		for _, g := range rule.APIGroups {
			for _, res := range rule.Resources {
				got[g+"/"+res] = append(got[g+"/"+res], rule.Verbs...)
			}
		}
	}
	for gr, verbs := range got {
		slices.Sort(verbs)
		if want, ok := unscopedRules[gr]; !ok || !slices.Equal(slices.Compact(verbs), want) {
			t.Errorf("unscoped %s: %v, want %v", gr, verbs, want)
		}
	}
	for gr := range unscopedRules {
		if _, ok := got[gr]; !ok {
			t.Errorf("unscopedRules lists %s, which the role no longer grants", gr)
		}
	}
}

// operatorCall is one request the reconciler made.
type operatorCall struct {
	verb, group, resource, namespace, name string
}

func callOf(scheme *runtime.Scheme, verb string, obj runtime.Object, namespace, name string) (operatorCall, bool) {
	gvk, err := apiutil.GVKForObject(obj, scheme)
	if err != nil {
		if u, ok := obj.(interface {
			GroupVersionKind() schema.GroupVersionKind
		}); ok {
			gvk = u.GroupVersionKind()
		} else {
			return operatorCall{}, false
		}
	}
	gvk.Kind = strings.TrimSuffix(gvk.Kind, "List")
	resource, _ := apimeta.UnsafeGuessKindToResource(gvk)
	return operatorCall{verb, gvk.Group, resource.Resource, namespace, name}, true
}

// recordCalls wraps the fake client so every apply, patch, read and list the
// reconciler makes lands in calls.
func recordCalls(calls *[]operatorCall) interceptor.Funcs {
	return interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if c, ok := callOf(cl.Scheme(), "patch", obj, obj.GetNamespace(), obj.GetName()); ok {
				// A server-side apply is authorized as create, with the name, when
				// the object is absent.
				if patch.Type() == types.ApplyPatchType {
					cc := c
					cc.verb = "create"
					*calls = append(*calls, cc)
				}
				*calls = append(*calls, c)
			}
			return cl.Patch(ctx, obj, patch, opts...)
		},
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if c, ok := callOf(cl.Scheme(), "get", obj, key.Namespace, key.Name); ok {
				*calls = append(*calls, c)
			}
			return cl.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if c, ok := callOf(cl.Scheme(), "list", list, (&client.ListOptions{}).ApplyOptions(opts).Namespace, ""); ok {
				*calls = append(*calls, c)
			}
			return cl.List(ctx, list, opts...)
		},
	}
}

// Every write and read the reconciler makes is granted by name: server-side
// apply is authorized as create with the object's name, so a name missing from
// the grant is an apply the apiserver refuses. An admin-named forge credential
// counts only through the Role a cluster admin installs for it.
func TestApplyGrantNamesEveryRenderedObject(t *testing.T) {
	role := readOperatorRole(t)
	forgeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer forgeDown.Close()
	byo, creds := byoForgeCR(forgeDown.URL)
	withIngress := managedForgeCR()
	withIngress.Spec.Ingress.Host = "dotvirt.example"
	withGateway := managedForgeCR()
	withGateway.Spec.Ingress = dotvirtv1alpha1.IngressSpec{Host: "dotvirt.example", Gateway: &dotvirtv1alpha1.GatewayRef{Name: "edge"}}
	// The router's CA, which the managed forge's trust in Argo copies.
	ingressCA := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "default-ingress-cert", Namespace: "openshift-config-managed"},
		Data:       map[string]string{"ca-bundle.crt": "PEM"},
	}
	sso := testCR()
	sso.Spec.Auth.OpenShiftSSO = new(true)
	// A managed forge that never answers fails its phase, before the Argo phase
	// merges the forge's host into Argo's trust: that step runs on its own.
	trust := func(r *DotvirtReconciler, dv *dotvirtv1alpha1.Dotvirt) {
		if err := r.ensureForgeTLSTrust(context.Background(), dv, phaseCtx(r, dv)); err != nil {
			t.Error(err)
		}
	}
	seen := map[string]bool{}
	for _, pass := range []struct {
		name     string
		dv       *dotvirtv1alpha1.Dotvirt
		objs     []client.Object
		platform platform.Platform
		dryRun   bool
		run      func(*DotvirtReconciler, *dotvirtv1alpha1.Dotvirt)
	}{
		{"BYO forge on OpenShift", byo, []client.Object{creds}, platform.OpenShift, false, nil},
		{"managed forge on OpenShift", managedForgeCR(), []client.Object{ingressCA}, platform.OpenShift, false, nil},
		{"managed forge, dry run", managedForgeCR(), []client.Object{ingressCA}, platform.OpenShift, true, nil},
		{"managed forge's trust in Argo", managedForgeCR(), []client.Object{ingressCA}, platform.OpenShift, false, trust},
		{"Ingresses", withIngress, nil, platform.Kubernetes, true, nil},
		{"HTTPRoutes", withGateway, nil, platform.Kubernetes, true, nil},
		{"OpenShift SSO", sso, nil, platform.OpenShift, false, nil},
	} {
		var calls []operatorCall
		c := testBuilder(t).WithObjects(append(pass.objs, pass.dv)...).WithInterceptorFuncs(recordCalls(&calls)).Build()
		r := newReconciler(c, depsOK)
		r.Platform, r.DryRun = pass.platform, pass.dryRun
		if pass.run != nil {
			pass.run(r, pass.dv)
		} else {
			// What ran up to a phase that fails is what is checked.
			_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pass.dv)})
		}
		grant, _ := install.ForgeCredentialGrant(pass.dv, "dotvirt-operator")
		for _, call := range calls {
			if call.group == "dotvirt.io" {
				continue
			}
			gr := call.group + "/" + call.resource
			seen[gr+"/"+call.name] = true
			unscoped := func(rule string) bool {
				_, ok := unscopedRules[rule]
				return ok
			}
			if grant != nil && call.namespace == grant.Namespace && operatorRole(grant.Rules).allows(call.verb, call.group, call.resource, call.name, unscoped) {
				continue
			}
			if !role.allows(call.verb, call.group, call.resource, call.name, unscoped) {
				t.Errorf("%s: %s %s %s/%s is not granted by name", pass.name, call.verb, gr, call.namespace, call.name)
			}
		}
	}
	for _, want := range []string{
		"/secrets/" + install.SessionSecretName,
		"/secrets/" + install.OAuthSecretName,
		"/secrets/" + install.ForgejoAdminSecret,
		"/secrets/" + install.RepoCredsName,
		"/configmaps/" + install.AppsetConfigMapName,
		"/configmaps/default-ingress-cert",
		"/configmaps/argocd-tls-certs-cm",
		"/configmaps/" + install.IngressCAConfigMap,
		"route.openshift.io/routes/" + install.AppName,
		"networking.k8s.io/ingresses/" + install.AppName,
		"gateway.networking.k8s.io/httproutes/" + install.AppName,
		"networking.k8s.io/networkpolicies/" + install.AppName,
		"rbac.authorization.k8s.io/clusterrolebindings/dotvirt",
		"argoproj.io/applications/" + install.PlatformApplicationName,
	} {
		if !seen[want] {
			t.Errorf("no pass touches %s, so its grant goes unchecked", want)
		}
	}
}

// The Secrets the operator's role names are the ones install treats as needing
// no grant: a name only one side knows is a credential the operator either
// cannot read or asks an admin to grant for nothing.
func TestOperatorSecretNamesAreTheRolesSecrets(t *testing.T) {
	var named []string
	for _, rule := range readOperatorRole(t) {
		if slices.Contains(rule.Resources, "secrets") && slices.Contains(rule.Verbs, "get") {
			named = append(named, rule.ResourceNames...)
		}
	}
	slices.Sort(named)
	want := slices.Sorted(slices.Values(install.OperatorSecretNames))
	if !slices.Equal(named, want) {
		t.Errorf("role's Secret names %v, install.OperatorSecretNames %v", named, want)
	}
}
