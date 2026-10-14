package controller

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

// operatorUser is the identity the generated ClusterRole is bound to; the
// manager runs as it, so any verb missing from config/rbac fails the suite.
const operatorUser = "system:serviceaccount:dotvirt-operator:dotvirt-operator"

// operatorFieldManager is install.Apply's field owner.
const operatorFieldManager = "dotvirt-operator"

// TestEnvtest runs the reconciler against a real apiserver: server-side apply
// semantics and the least-privilege RBAC the fake client cannot check. It
// needs the envtest binaries (`make -C operator test-envtest`); plain
// `go test` skips it so the unit suite stays hermetic.
func TestEnvtest(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset; run `make -C operator test-envtest`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		CRDs:                  argoStubCRDs(),
		ErrorIfCRDPathMissing: true,
	}
	adminCfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Logf("stop envtest: %v", err)
		}
	})
	scheme := testScheme(t)
	admin, err := client.New(adminCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	for _, ns := range []string{"dotvirt", "argocd"} {
		mustCreate(t, admin, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	}
	for _, f := range []string{"role.yaml", "operand_roles.yaml"} {
		for _, role := range readClusterRoles(t, filepath.Join("..", "..", "config", "rbac", f)) {
			mustCreate(t, admin, role)
		}
	}
	mustCreate(t, admin, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "dotvirt-operator"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "dotvirt-operator"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: operatorUser}},
	})
	user, err := env.AddUser(envtest.User{Name: operatorUser}, nil)
	if err != nil {
		t.Fatal(err)
	}
	opCfg := user.Config()

	t.Run("role grants no escalation", func(t *testing.T) {
		kube := kubernetes.NewForConfigOrDie(opCfg)
		for _, c := range []struct {
			attrs authzv1.ResourceAttributes
			want  bool
		}{
			{authzv1.ResourceAttributes{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "dotvirt"}, true},
			{authzv1.ResourceAttributes{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "cluster-admin"}, false},
			{authzv1.ResourceAttributes{Verb: "escalate", Group: "rbac.authorization.k8s.io", Resource: "clusterroles"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles"}, false},
			{authzv1.ResourceAttributes{Verb: "update", Group: "apps", Resource: "deployments", Namespace: "dotvirt"}, false},
			{authzv1.ResourceAttributes{Verb: "watch", Resource: "configmaps"}, false},
		} {
			res, err := kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx,
				&authzv1.SelfSubjectAccessReview{Spec: authzv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &c.attrs}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status.Allowed != c.want {
				t.Errorf("%s %s/%s %s: allowed=%v, want %v", c.attrs.Verb, c.attrs.Group, c.attrs.Resource, c.attrs.Name, res.Status.Allowed, c.want)
			}
		}
		// The bind grant is what lets the operator create its bindings without
		// holding the operand roles' rules; a binding to any other role is refused.
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "escalation-probe"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: operatorUser}},
		}
		if _, err := kube.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); !apierrors.IsForbidden(err) {
			t.Errorf("binding cluster-admin: err=%v, want Forbidden", err)
		}
	})

	// One manager for the remaining subtests: its cleanup belongs to the parent.
	runManager(t, opCfg, scheme)

	t.Run("install reconciles to ready as the operator", func(t *testing.T) {
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
		mustCreate(t, admin, dv)
		key := types.NamespacedName{Namespace: "dotvirt", Name: install.AppName}

		var dep appsv1.Deployment
		eventually(t, "operand Deployment applied", func() bool { return admin.Get(ctx, key, &dep) == nil })
		// envtest runs no Deployment controller: stand in for the pod coming up.
		dep.Status.Replicas, dep.Status.ReadyReplicas = 1, 1
		if err := admin.Status().Update(ctx, &dep); err != nil {
			t.Fatal(err)
		}
		eventually(t, "phase Ready", func() bool {
			got := &dotvirtv1alpha1.Dotvirt{}
			return admin.Get(ctx, client.ObjectKeyFromObject(dv), got) == nil && got.Status.Phase == dotvirtv1alpha1.PhaseReady
		})
		got := &dotvirtv1alpha1.Dotvirt{}
		if err := admin.Get(ctx, client.ObjectKeyFromObject(dv), got); err != nil {
			t.Fatal(err)
		}
		for _, c := range got.Status.Conditions {
			// No forge in this install: ForgeReady says so, and nothing else may fail.
			if c.Status == metav1.ConditionFalse && c.Type != dotvirtv1alpha1.ConditionForgeReady {
				t.Errorf("condition %s False: %s", c.Type, c.Message)
			}
		}

		var crbs rbacv1.ClusterRoleBindingList
		if err := admin.List(ctx, &crbs, client.MatchingLabels{install.InstanceLabel: dv.Name}); err != nil {
			t.Fatal(err)
		}
		if len(crbs.Items) != 3 {
			t.Errorf("instance ClusterRoleBindings = %d, want 3", len(crbs.Items))
		}
		appset := install.ApplicationSet(got, "argocd")
		if err := admin.Get(ctx, client.ObjectKeyFromObject(appset), argoObj("ApplicationSet", "", "", nil)); err != nil {
			t.Errorf("ApplicationSet: %v", err)
		}
	})

	t.Run("apply reclaims owned fields and keeps foreign ones", func(t *testing.T) {
		key := types.NamespacedName{Namespace: "dotvirt", Name: install.AppName}
		var dep appsv1.Deployment
		if err := admin.Get(ctx, key, &dep); err != nil {
			t.Fatal(err)
		}
		if !managedBy(dep.ManagedFields, operatorFieldManager, metav1.ManagedFieldsOperationApply) {
			t.Fatalf("Deployment fields not applied by %q: %+v", operatorFieldManager, dep.ManagedFields)
		}
		image := dep.Spec.Template.Spec.Containers[0].Image
		dep.Spec.Template.Spec.Containers[0].Image = "example.invalid/drift:1"
		if dep.Annotations == nil {
			dep.Annotations = map[string]string{}
		}
		dep.Annotations["example.com/owner-note"] = "kept"
		if err := admin.Update(ctx, &dep, client.FieldOwner("hand-edit")); err != nil {
			t.Fatal(err)
		}
		// The owned-Deployment watch re-runs the pass.
		eventually(t, "image restored", func() bool {
			return admin.Get(ctx, key, &dep) == nil && dep.Spec.Template.Spec.Containers[0].Image == image
		})
		if dep.Annotations["example.com/owner-note"] != "kept" {
			t.Errorf("apply dropped a field it does not own: %v", dep.Annotations)
		}
	})

	t.Run("deletion cleans up as the operator", func(t *testing.T) {
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
		if err := admin.Delete(ctx, dv); err != nil {
			t.Fatal(err)
		}
		eventually(t, "CR gone", func() bool {
			return apierrors.IsNotFound(admin.Get(ctx, client.ObjectKeyFromObject(dv), &dotvirtv1alpha1.Dotvirt{}))
		})
		var crbs rbacv1.ClusterRoleBindingList
		if err := admin.List(ctx, &crbs, client.MatchingLabels{install.InstanceLabel: "x"}); err != nil {
			t.Fatal(err)
		}
		if len(crbs.Items) != 0 {
			t.Errorf("instance ClusterRoleBindings left: %d", len(crbs.Items))
		}
		ul := &unstructured.UnstructuredList{}
		ul.SetGroupVersionKind(install.ArgoGVK("ApplicationSetList"))
		if err := admin.List(ctx, ul, client.InNamespace("argocd"), client.MatchingLabels{install.InstanceLabel: "x"}); err != nil {
			t.Fatal(err)
		}
		if len(ul.Items) != 0 {
			t.Errorf("instance ApplicationSets left: %d", len(ul.Items))
		}
	})
}

// runManager starts the controller as cmd/main.go wires it, under cfg.
func runManager(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme) {
	t.Helper()
	hasInstance, _ := labels.NewRequirement(install.InstanceLabel, selection.Exists, nil)
	instanceOnly := cache.ByObject{Label: labels.NewSelector().Add(*hasInstance)}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&corev1.Secret{}:     instanceOnly,
			&appsv1.Deployment{}: instanceOnly,
		}},
		Client: client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.ConfigMap{}, &corev1.Secret{}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &DotvirtReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Config:    mgr.GetConfig(),
		Platform:  platform.Kubernetes,
		APIReader: mgr.GetAPIReader(),
		probe:     depsOK,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("manager: %v", err)
		}
	})
}

// argoStubCRDs registers the argoproj.io kinds the install applies, schemaless:
// the suite checks apply and RBAC, not Argo's own validation.
func argoStubCRDs() []*apiextensionsv1.CustomResourceDefinition {
	preserve := true
	var out []*apiextensionsv1.CustomResourceDefinition
	for kind, plural := range map[string]string{"AppProject": "appprojects", "Application": "applications", "ApplicationSet": "applicationsets"} {
		out = append(out, &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: plural + ".argoproj.io"},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "argoproj.io",
				Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: kind, ListKind: kind + "List", Plural: plural, Singular: strings.ToLower(kind)},
				Scope: apiextensionsv1.NamespaceScoped,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name: "v1alpha1", Served: true, Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object", XPreserveUnknownFields: &preserve,
					}},
				}},
			},
		})
	}
	return out
}

func readClusterRoles(t *testing.T, path string) []*rbacv1.ClusterRole {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	dec := utilyaml.NewYAMLOrJSONDecoder(f, 4096)
	var out []*rbacv1.ClusterRole
	for {
		role := &rbacv1.ClusterRole{}
		if err := dec.Decode(role); errors.Is(err, io.EOF) {
			return out
		} else if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if role.Name != "" {
			out = append(out, role)
		}
	}
}

func mustCreate(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	if err := c.Create(context.Background(), obj); err != nil {
		t.Fatalf("create %T %s: %v", obj, obj.GetName(), err)
	}
}

func managedBy(fields []metav1.ManagedFieldsEntry, manager string, op metav1.ManagedFieldsOperationType) bool {
	for _, f := range fields {
		if f.Manager == manager && f.Operation == op {
			return true
		}
	}
	return false
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
