package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/cel/openapi/resolver"
	admissionregistrationv1ac "k8s.io/client-go/applyconfigurations/admissionregistration/v1"
	rbacv1ac "k8s.io/client-go/applyconfigurations/rbac/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/deps"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/adminobjects"
	"github.com/epheo/dotvirt/pkg/tenancy"
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
		CRDs:                  append(argoStubCRDs(), exposureStubCRDs()...),
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
	mustCreate(t, admin, projectNS("team-a", "team-a", "https://forge.example/dotvirt/team-a.git"))
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
			// Deletes are scoped to the names the install renders.
			{authzv1.ResourceAttributes{Verb: "delete", Resource: "secrets", Namespace: "argocd", Name: install.AppsetSecretName}, true},
			{authzv1.ResourceAttributes{Verb: "delete", Resource: "secrets", Namespace: "argocd", Name: "argocd-secret"}, false},
			{authzv1.ResourceAttributes{Verb: "deletecollection", Resource: "secrets", Namespace: "argocd"}, false},
			{authzv1.ResourceAttributes{Verb: "delete", Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "dotvirt"}, true},
			{authzv1.ResourceAttributes{Verb: "delete", Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "cluster-admin"}, false},
			{authzv1.ResourceAttributes{Verb: "delete", Group: "argoproj.io", Resource: "applications", Namespace: "argocd", Name: "some-tenant"}, false},
			// Tenant AppProjects are named after their projects; prune bounds the
			// unscoped delete to this install's labels.
			{authzv1.ResourceAttributes{Verb: "delete", Group: "argoproj.io", Resource: "appprojects", Namespace: "argocd", Name: "dotvirt-p-any"}, true},
			{authzv1.ResourceAttributes{Verb: "deletecollection", Group: "argoproj.io", Resource: "appprojects", Namespace: "argocd"}, false},
			{authzv1.ResourceAttributes{Verb: "watch", Resource: "namespaces"}, true},
			{authzv1.ResourceAttributes{Verb: "patch", Resource: "namespaces", Name: "team-a"}, false},
			// Sync impersonation: the reviews, the one key on the instance, the
			// platform account's binding; never the accounts' delete (the GC
			// removes them with their AppProjects) or the roles they are granted.
			{authzv1.ResourceAttributes{Verb: "create", Group: "authorization.k8s.io", Resource: "subjectaccessreviews"}, true},
			{authzv1.ResourceAttributes{Verb: "patch", Group: "argoproj.io", Resource: "argocds", Namespace: "openshift-gitops", Name: "openshift-gitops"}, true},
			{authzv1.ResourceAttributes{Verb: "get", Group: "argoproj.io", Resource: "argocds", Namespace: "argocd", Name: "argocd"}, true},
			{authzv1.ResourceAttributes{Verb: "patch", Group: "argoproj.io", Resource: "argocds", Namespace: "argocd", Name: "other"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Group: "argoproj.io", Resource: "argocds", Namespace: "argocd"}, false},
			{authzv1.ResourceAttributes{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: tenancy.PlatformSyncAccount}, true},
			{authzv1.ResourceAttributes{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "admin"}, false},
			{authzv1.ResourceAttributes{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: tenancy.TenantEgressRole}, false},
			{authzv1.ResourceAttributes{Verb: "delete", Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: tenancy.PlatformSyncAccount}, true},
			{authzv1.ResourceAttributes{Verb: "delete", Resource: "serviceaccounts", Namespace: "argocd", Name: "dotvirt-p-team-a"}, false},
			// RoleBindings are read one by one, never listed or watched. The one the
			// install writes is created (server-side apply authorizes create with the
			// name), patched and deleted by name, and no other.
			{authzv1.ResourceAttributes{Verb: "get", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: "team-a", Name: "team-a-admins"}, true},
			{authzv1.ResourceAttributes{Verb: "list", Group: "rbac.authorization.k8s.io", Resource: "rolebindings"}, false},
			// The forge credential's grant is watched by name, never written.
			{authzv1.ResourceAttributes{Verb: "list", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Name: install.ForgeCredentialGrantName}, true},
			{authzv1.ResourceAttributes{Verb: "watch", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Name: install.ForgeCredentialGrantName}, true},
			{authzv1.ResourceAttributes{Verb: "list", Group: "rbac.authorization.k8s.io", Resource: "roles", Name: install.ForgeCredentialGrantName}, true},
			{authzv1.ResourceAttributes{Verb: "watch", Group: "rbac.authorization.k8s.io", Resource: "roles", Name: install.ForgeCredentialGrantName}, true},
			{authzv1.ResourceAttributes{Verb: "list", Group: "rbac.authorization.k8s.io", Resource: "roles"}, false},
			{authzv1.ResourceAttributes{Verb: "get", Group: "rbac.authorization.k8s.io", Resource: "roles", Namespace: "dotvirt", Name: install.ForgeCredentialGrantName}, true},
			{authzv1.ResourceAttributes{Verb: "get", Group: "rbac.authorization.k8s.io", Resource: "roles", Namespace: "dotvirt", Name: "admin"}, false},
			// Before minting into an admin-named credential, the operator asks
			// whether it may store it.
			{authzv1.ResourceAttributes{Verb: "create", Group: "authorization.k8s.io", Resource: "selfsubjectaccessreviews"}, true},
			{authzv1.ResourceAttributes{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: "dotvirt", Name: install.ForgeCredentialGrantName}, false},
			{authzv1.ResourceAttributes{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "roles", Namespace: "dotvirt", Name: install.ForgeCredentialGrantName}, false},
			{authzv1.ResourceAttributes{Verb: "watch", Group: "rbac.authorization.k8s.io", Resource: "rolebindings"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: "argocd", Name: install.SyncRequestName}, true},
			{authzv1.ResourceAttributes{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: "team-a"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: "team-a", Name: "team-a-admins"}, false},
			{authzv1.ResourceAttributes{Verb: "patch", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: "argocd", Name: install.SyncRequestName}, true},
			{authzv1.ResourceAttributes{Verb: "patch", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: "team-a", Name: "team-a-admins"}, false},
			{authzv1.ResourceAttributes{Verb: "delete", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: "argocd", Name: install.SyncRequestName}, true},
			{authzv1.ResourceAttributes{Verb: "delete", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: "team-a", Name: "team-a-admins"}, false},
			// Proved by this row alone: the operator also holds the role's one rule,
			// so the escalation check would admit its binding without bind.
			{authzv1.ResourceAttributes{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: install.SyncRequestName}, true},
			// The operator guard: read by name to report it, never written.
			{authzv1.ResourceAttributes{Verb: "get", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Name: adminobjects.GuardSecretsName}, true},
			{authzv1.ResourceAttributes{Verb: "get", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicybindings", Name: adminobjects.GuardDeploymentsName}, true},
			{authzv1.ResourceAttributes{Verb: "get", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Name: adminobjects.GuardBindingsName}, true},
			{authzv1.ResourceAttributes{Verb: "get", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Name: adminobjects.GuardInstallName}, true},
			// Its install: the finalizer by patch, never a whole-object update.
			{authzv1.ResourceAttributes{Verb: "patch", Group: "dotvirt.io", Resource: "dotvirts", Namespace: "dotvirt", Name: "x"}, true},
			{authzv1.ResourceAttributes{Verb: "update", Group: "dotvirt.io", Resource: "dotvirts", Namespace: "dotvirt", Name: "x"}, false},
			{authzv1.ResourceAttributes{Verb: "get", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Name: "other"}, false},
			{authzv1.ResourceAttributes{Verb: "list", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicybindings"}, false},
			{authzv1.ResourceAttributes{Verb: "patch", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Name: adminobjects.GuardSecretsName}, false},
			{authzv1.ResourceAttributes{Verb: "patch", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicybindings", Name: adminobjects.GuardSecretsName}, false},
			{authzv1.ResourceAttributes{Verb: "update", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Name: adminobjects.GuardSecretsName}, false},
			{authzv1.ResourceAttributes{Verb: "update", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicybindings", Name: adminobjects.GuardSecretsName}, false},
			{authzv1.ResourceAttributes{Verb: "delete", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Name: adminobjects.GuardSecretsName}, false},
			{authzv1.ResourceAttributes{Verb: "delete", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicybindings", Name: adminobjects.GuardSecretsName}, false},
			// Secrets are never listed or watched, and read or written by name.
			{authzv1.ResourceAttributes{Verb: "list", Resource: "secrets"}, false},
			{authzv1.ResourceAttributes{Verb: "watch", Resource: "secrets"}, false},
			{authzv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: "argocd", Name: install.ArgoSecretName}, true},
			{authzv1.ResourceAttributes{Verb: "patch", Resource: "secrets", Namespace: "dotvirt", Name: install.SessionSecretName}, true},
			{authzv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: "kube-system", Name: "some-token"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Resource: "secrets", Namespace: "kube-system", Name: "some-token"}, false},
			{authzv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: "dotvirt", Name: "byo-creds"}, false},
			// ConfigMaps, all by name: argocd-cm listed and watched by name for the
			// cache, Argo's RBAC settings never touched.
			{authzv1.ResourceAttributes{Verb: "list", Resource: "configmaps"}, false},
			{authzv1.ResourceAttributes{Verb: "list", Resource: "configmaps", Name: "argocd-cm"}, true},
			{authzv1.ResourceAttributes{Verb: "watch", Resource: "configmaps", Name: "argocd-cm"}, true},
			{authzv1.ResourceAttributes{Verb: "patch", Resource: "configmaps", Namespace: "argocd", Name: "argocd-tls-certs-cm"}, true},
			{authzv1.ResourceAttributes{Verb: "get", Resource: "configmaps", Namespace: "argocd", Name: "argocd-rbac-cm"}, false},
			{authzv1.ResourceAttributes{Verb: "patch", Resource: "configmaps", Namespace: "argocd", Name: "argocd-rbac-cm"}, false},
			// Bindings, the operand and the Argo objects: the names the install
			// renders, never listed.
			{authzv1.ResourceAttributes{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "dotvirt"}, true},
			{authzv1.ResourceAttributes{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "escalation-probe"}, false},
			{authzv1.ResourceAttributes{Verb: "list", Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Resource: "serviceaccounts", Namespace: "kube-system", Name: "builder"}, false},
			{authzv1.ResourceAttributes{Verb: "list", Resource: "services"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Group: "apps", Resource: "deployments", Namespace: "dotvirt", Name: "other"}, false},
			{authzv1.ResourceAttributes{Verb: "list", Group: "networking.k8s.io", Resource: "networkpolicies"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Group: "argoproj.io", Resource: "applicationsets", Namespace: "argocd", Name: install.ApplicationSetName}, true},
			{authzv1.ResourceAttributes{Verb: "list", Group: "argoproj.io", Resource: "applicationsets"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Group: "argoproj.io", Resource: "applications", Namespace: "argocd", Name: "other"}, false},
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
		// holding the operand roles' rules; under a name it may create, as it
		// creates them (server-side apply), a binding to any other role is
		// refused by the escalation check.
		crb := rbacv1ac.ClusterRoleBinding("dotvirt").
			WithRoleRef(rbacv1ac.RoleRef().WithAPIGroup(rbacv1.GroupName).WithKind("ClusterRole").WithName("cluster-admin")).
			WithSubjects(rbacv1ac.Subject().WithKind(rbacv1.UserKind).WithName(operatorUser))
		if _, err := kube.RbacV1().ClusterRoleBindings().Apply(ctx, crb, metav1.ApplyOptions{FieldManager: "dotvirt-operator"}); !apierrors.IsForbidden(err) ||
			!strings.Contains(err.Error(), "attempting to grant RBAC permissions not currently held") {
			t.Errorf("binding cluster-admin: err=%v, want the escalation check's Forbidden", err)
		}
		// Under the one name it may create, as the operator creates it (server-side
		// apply, authorized with that name), a RoleBinding to another role is still
		// refused: RBAC's escalation check stops it, not the name. A POST create
		// would carry no name and stop at authorization instead. cluster-admin, not
		// admin: envtest runs no aggregation controller, so its admin role is empty
		// and anyone may bind it.
		rb := rbacv1ac.RoleBinding(install.SyncRequestName, "team-a").
			WithRoleRef(rbacv1ac.RoleRef().WithAPIGroup(rbacv1.GroupName).WithKind("ClusterRole").WithName("cluster-admin")).
			WithSubjects(rbacv1ac.Subject().WithKind(rbacv1.UserKind).WithName(operatorUser))
		_, err := kube.RbacV1().RoleBindings("team-a").Apply(ctx, rb, metav1.ApplyOptions{FieldManager: "dotvirt-operator"})
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "attempting to grant RBAC permissions not currently held") {
			t.Errorf("binding cluster-admin in team-a: err=%v, want the escalation check's Forbidden", err)
		}
	})

	// One manager for the remaining subtests: its cleanup belongs to the parent.
	passes, ops := runManager(t, opCfg, scheme)

	t.Run("install reconciles to ready as the operator", func(t *testing.T) {
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
		// The stub ArgoCD's controller holds no rights, and its own subtest below
		// turns impersonation on.
		dv.Spec.ArgoCD.SyncImpersonation.Enabled = new(false)
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
			// No forge in this install: ForgeReady says so; no admin installed the
			// operator guard yet: OperatorGuard says so. Nothing else may fail.
			if c.Status == metav1.ConditionFalse && c.Type != dotvirtv1alpha1.ConditionForgeReady && c.Type != dotvirtv1alpha1.ConditionOperatorGuard {
				t.Errorf("condition %s False: %s", c.Type, c.Message)
			}
		}

		var crbs rbacv1.ClusterRoleBindingList
		if err := admin.List(ctx, &crbs, client.MatchingLabels{install.InstanceLabel: dv.Name}); err != nil {
			t.Fatal(err)
		}
		// dotvirt, dotvirt-argocd-apply, dotvirt-argocd-read, the platform
		// network admins, and the storage catalog off OpenShift.
		if len(crbs.Items) != 5 {
			t.Errorf("instance ClusterRoleBindings = %d, want 5", len(crbs.Items))
		}
		// dotvirt's ServiceAccount reads Applications everywhere and patches
		// them only in the ArgoCD namespace.
		eventually(t, "sync-request RoleBinding", func() bool {
			return admin.Get(ctx, types.NamespacedName{Namespace: "argocd", Name: install.SyncRequestName}, &rbacv1.RoleBinding{}) == nil
		})
		adminKube := kubernetes.NewForConfigOrDie(adminCfg)
		sa := "system:serviceaccount:" + dv.Namespace + ":" + install.AppName
		for _, c := range []struct {
			verb, ns string
			want     bool
		}{
			{"patch", "argocd", true},
			{"patch", "team-a", false},
			{"patch", "", false},
			{"update", "argocd", false},
			{"list", "", true},
		} {
			res, err := adminKube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{
				User:               sa,
				Groups:             []string{"system:serviceaccounts", "system:serviceaccounts:" + dv.Namespace},
				ResourceAttributes: &authzv1.ResourceAttributes{Verb: c.verb, Group: "argoproj.io", Resource: "applications", Namespace: c.ns},
			}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status.Allowed != c.want {
				t.Errorf("dotvirt %s applications in %q: allowed=%v, want %v", c.verb, c.ns, res.Status.Allowed, c.want)
			}
		}
		appset := install.ApplicationSet(got, "argocd", nil, nil)
		if err := admin.Get(ctx, client.ObjectKeyFromObject(appset), argoObj("ApplicationSet", "", "", nil)); err != nil {
			t.Errorf("ApplicationSet: %v", err)
		}
		if err := admin.Get(ctx, types.NamespacedName{Namespace: "argocd", Name: "dotvirt-p-team-a"}, argoObj("AppProject", "", "", nil)); err != nil {
			t.Errorf("tenant AppProject: %v", err)
		}
	})

	t.Run("the operator guard bounds the operator once an admin installs it", func(t *testing.T) {
		key := types.NamespacedName{Namespace: "dotvirt", Name: "x"}
		guardCond := func(want string) bool {
			got := &dotvirtv1alpha1.Dotvirt{}
			if admin.Get(ctx, key, got) != nil {
				return false
			}
			c := apimeta.FindStatusCondition(got.Status.Conditions, dotvirtv1alpha1.ConditionOperatorGuard)
			return c != nil && c.Reason == want && len(got.Status.AdminObjects) == 2*len(adminobjects.GuardNames)
		}
		eventually(t, "OperatorGuard NotInstalled", func() bool { return guardCond(string(adminobjects.NotInstalled)) })

		// The console's install, as a cluster admin: its own render, from the
		// pins it is configured with, server-side applied.
		pins := adminobjects.Params{Operator: operatorUser, Namespace: "dotvirt", ArgoNamespace: "argocd", ArgoController: "argocd-application-controller",
			ForgeSecret: adminobjects.DefaultForgeSecret}
		var objs []*unstructured.Unstructured
		installGuard := func(p adminobjects.Params) {
			objs = nil
			for _, o := range adminobjects.Render(p) {
				if _, ok := adminobjects.Allow(adminobjects.Allowed(p.Namespace), o.Object); !ok {
					t.Fatalf("the console would refuse its own %s %s", o.Object.GetKind(), o.Object.GetName())
				}
				if err := admin.Apply(ctx, client.ApplyConfigurationFromUnstructured(o.Object.DeepCopy()), client.FieldOwner("dotvirt"), client.ForceOwnership); err != nil {
					t.Fatalf("install %s: %v", o.Object.GetName(), err)
				}
				objs = append(objs, o.Object)
			}
		}
		installGuard(pins)
		// Later subtests write as the operator into other ArgoCD namespaces,
		// which this guard rightly refuses: an admin removes it, as uninstall
		// leaves them to, whatever this subtest found.
		t.Cleanup(func() {
			for _, o := range objs {
				_ = admin.Delete(context.Background(), o)
			}
			eventually(t, "the guard lifted", func() bool {
				m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&corev1.Secret{
					TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
					ObjectMeta: metav1.ObjectMeta{Name: install.OAuthSecretName, Namespace: "team-a"},
				})
				c, err := client.New(opCfg, client.Options{Scheme: scheme})
				if err != nil {
					return false
				}
				return c.Apply(context.Background(), client.ApplyConfigurationFromUnstructured(&unstructured.Unstructured{Object: m}),
					client.FieldOwner(operatorFieldManager), client.ForceOwnership, client.DryRunAll) == nil
			})
		})
		// Installing the guard changes nothing the operator watches, so a spec
		// edit runs the next pass, as the drift timer would.
		patchCR := func() {
			got := &dotvirtv1alpha1.Dotvirt{}
			if err := admin.Get(ctx, key, got); err != nil {
				t.Fatal(err)
			}
			mp := client.MergeFrom(got.DeepCopy())
			got.Spec.Branding.ProductName = fmt.Sprint("Guarded ", time.Now().UnixNano())
			if err := admin.Patch(ctx, got, mp); err != nil {
				t.Fatal(err)
			}
		}
		patchCR()
		eventually(t, "OperatorGuard Installed", func() bool { return guardCond(string(adminobjects.Installed)) })

		// Each policy type-checks clean against the one kind it matches, as the
		// controller-manager checks it (envtest runs none to fill the status):
		// a rule naming another kind's fields leaves a warning per rule and kind.
		checker := &validating.TypeChecker{
			SchemaResolver: &resolver.ClientDiscoveryResolver{Discovery: discovery.NewDiscoveryClientForConfigOrDie(adminCfg)},
			RestMapper:     admin.RESTMapper(),
		}
		for _, o := range adminobjects.Render(pins) {
			if o.Object.GetKind() != "ValidatingAdmissionPolicy" {
				continue
			}
			var pol admissionregistrationv1.ValidatingAdmissionPolicy
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object.Object, &pol); err != nil {
				t.Fatal(err)
			}
			if w := checker.Check(&pol); len(w) > 0 {
				t.Errorf("%s type-checks with %d warnings, the first: %s", pol.Name, len(w), w[0].Warning)
			}
		}
		mixed := &admissionregistrationv1.ValidatingAdmissionPolicy{Spec: admissionregistrationv1.ValidatingAdmissionPolicySpec{
			MatchConstraints: &admissionregistrationv1.MatchResources{ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{
				RuleWithOperations: admissionregistrationv1.RuleWithOperations{Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
					Rule: admissionregistrationv1.Rule{APIGroups: []string{"", "apps"}, APIVersions: []string{"v1"}, Resources: []string{"services", "deployments"}}},
			}}},
			Validations: []admissionregistrationv1.Validation{{Expression: `has(object.spec.template.spec.hostNetwork)`}},
		}}
		if len(checker.Check(mixed)) == 0 {
			t.Error("the type checker passes a rule naming a Deployment's field on Services; the check above proves nothing")
		}

		op := kubernetes.NewForConfigOrDie(opCfg)
		opClient, err := client.New(opCfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		refused := func(err error) bool {
			return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "dotvirt operator guard")
		}
		// What the operator writes, as it writes it: a server-side apply, which
		// its role authorizes by name, run dry so the probes leave nothing.
		dryApply := func(o client.Object) error {
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
			if err != nil {
				t.Fatal(err)
			}
			return opClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(&unstructured.Unstructured{Object: m}),
				client.FieldOwner(operatorFieldManager), client.ForceOwnership, client.DryRunAll)
		}
		// Secrets under names the operator's role grants but this install never
		// writes (no SSO, no managed forge), so only the guard stands in the way.
		secret := func(ns, name string, edit func(*corev1.Secret)) *corev1.Secret {
			s := &corev1.Secret{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				StringData: map[string]string{"k": "v"},
			}
			if edit != nil {
				edit(s)
			}
			return s
		}
		asToken := func(s *corev1.Secret) {
			s.Type = corev1.SecretTypeServiceAccountToken
			s.Annotations = map[string]string{corev1.ServiceAccountNameKey: "default"}
		}
		// The apiserver enforces a new policy once its informer has it.
		eventually(t, "the guard enforced", func() bool {
			return refused(dryApply(secret("dotvirt", install.OAuthSecretName, asToken)))
		})
		annotated := secret("dotvirt", install.OAuthSecretName, func(s *corev1.Secret) {
			s.Annotations = map[string]string{corev1.ServiceAccountNameKey: "default"}
		})
		if err := dryApply(annotated); !refused(err) {
			t.Errorf("operator's annotated Secret: err=%v, want the guard's Forbidden", err)
		}
		if err := dryApply(secret("team-a", install.OAuthSecretName, nil)); !refused(err) {
			t.Errorf("operator's Secret outside its namespaces: err=%v, want the guard's Forbidden", err)
		}
		for _, ns := range []string{"dotvirt", "argocd"} {
			if err := dryApply(secret(ns, install.ForgejoAdminSecret, nil)); err != nil {
				t.Errorf("operator's plain Secret in %s: %v", ns, err)
			}
		}
		token := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "guard-token", Annotations: map[string]string{corev1.ServiceAccountNameKey: "default"}},
			Type:       corev1.SecretTypeServiceAccountToken,
		}
		other, err := env.AddUser(envtest.User{Name: "system:serviceaccount:team-a:builder", Groups: []string{"system:masters"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kubernetes.NewForConfigOrDie(other.Config()).CoreV1().Secrets("team-a").Create(ctx, token, metav1.CreateOptions{}); err != nil {
			t.Errorf("another ServiceAccount's token Secret: %v", err)
		}

		dv := &dotvirtv1alpha1.Dotvirt{}
		if err := admin.Get(ctx, key, dv); err != nil {
			t.Fatal(err)
		}
		app := func() *appsv1.Deployment { return install.Deployment(dv, "argocd", nil) }
		forge := func() *appsv1.Deployment { return install.ForgejoDeployment(dv, false, "", "") }
		newDigest := app()
		newDigest.Spec.Template.Spec.Containers[0].Image = "quay.io/epheo/dotvirt@sha256:" + strings.Repeat("b", 64)
		newTag := forge()
		newTag.Spec.Template.Spec.Containers[0].Image = "codeberg.org/forgejo/forgejo:12-rootless"
		// A release that adds a console flag, or changes the forge's
		// bootstrap, upgrades without a new guard.
		newFlag := app()
		newFlag.Spec.Template.Spec.Containers[0].Args = append(newFlag.Spec.Template.Spec.Containers[0].Args, "-new-setting=1", "--git-poll-interval=10m")
		newBootstrap := forge()
		newBootstrap.Spec.Template.Spec.InitContainers[0].Command = []string{"sh", "-c", "forgejo migrate"}
		routerCA := app()
		rc := &routerCA.Spec.Template.Spec.Containers[0]
		rc.Env = append(rc.Env, corev1.EnvVar{Name: "DOTVIRT_OAUTH_CA", Value: adminobjects.IngressCAPath})
		for name, d := range map[string]*appsv1.Deployment{
			"the console as rendered": app(), "the forge as rendered": forge(),
			"the console trusting the router CA for SSO": routerCA,
			"a new release by digest":                    newDigest, "a new forge release by tag": newTag,
			"a release with a new console flag": newFlag, "a release with a new forge bootstrap": newBootstrap,
		} {
			if err := dryApply(d); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
		sc := func(d *appsv1.Deployment) *corev1.SecurityContext {
			return d.Spec.Template.Spec.Containers[0].SecurityContext
		}
		// withVolume gives the Deployment's first container v mounted by m, in
		// place of any volume or mount of their names.
		withVolume := func(v corev1.Volume, m corev1.VolumeMount) func(*appsv1.Deployment) {
			return func(d *appsv1.Deployment) {
				pod := &d.Spec.Template.Spec
				pod.Volumes = append(slices.DeleteFunc(slices.Clone(pod.Volumes), func(x corev1.Volume) bool { return x.Name == v.Name }), v)
				c := &pod.Containers[0]
				c.VolumeMounts = append(slices.DeleteFunc(slices.Clone(c.VolumeMounts), func(x corev1.VolumeMount) bool { return x.Name == m.Name }), m)
			}
		}
		configMapVol := func(name, cm string) corev1.Volume {
			return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cm}}}}
		}
		secretVol := func(name, secret string) corev1.Volume {
			return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret}}}
		}
		claimVol := func(name, claim string) corev1.Volume {
			return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}}
		}
		secretEnv := func(secret string) corev1.EnvVar {
			return corev1.EnvVar{Name: "LEAK", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: "secret"}}}
		}
		fromSecret := func(env, secret, key string) corev1.EnvVar {
			return corev1.EnvVar{Name: env, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: key}}}
		}
		// withEnv sets env on the first container, in place of any of its names.
		withEnv := func(env ...corev1.EnvVar) func(*appsv1.Deployment) {
			return func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(slices.DeleteFunc(slices.Clone(c.Env), func(x corev1.EnvVar) bool {
					return slices.ContainsFunc(env, func(e corev1.EnvVar) bool { return e.Name == x.Name })
				}), env...)
			}
		}
		for name, c := range map[string]struct {
			d    func() *appsv1.Deployment
			edit func(*appsv1.Deployment)
		}{
			"in another namespace": {app, func(d *appsv1.Deployment) { d.Namespace = "team-a" }},
			"as another account":   {app, func(d *appsv1.Deployment) { d.Spec.Template.Spec.ServiceAccountName = "default" }},
			"from another repository": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Image = "docker.io/library/nginx:1"
			}},
			"from a lookalike repository": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Image = "quay.io/epheo/dotvirt-evil:1"
			}},
			"with an init container from elsewhere": {forge, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.InitContainers[0].Image = "docker.io/library/busybox:1"
			}},
			"with a privileged init container": {forge, func(d *appsv1.Deployment) {
				s := d.Spec.Template.Spec.InitContainers[0].SecurityContext
				s.Privileged, s.AllowPrivilegeEscalation = new(true), nil
			}},
			"the console from the forge's repository": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Image = "codeberg.org/forgejo/forgejo:11-rootless"
			}},
			"the forge from the console's repository": {forge, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Image = "quay.io/epheo/dotvirt:latest"
			}},
			"the console with a command": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = []string{"sh", "-c", "env"}
			}},
			"the console skipping TLS no admin chose": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Args = append(d.Spec.Template.Spec.Containers[0].Args, "-insecure-tls")
			}},
			"the console skipping TLS by env": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, corev1.EnvVar{Name: "DOTVIRT_INSECURE_TLS", Value: "true"})
			}},
			"the console sending metrics queries elsewhere": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Args = append(d.Spec.Template.Spec.Containers[0].Args, "-metrics-url=https://evil.example")
			}},
			"the console sending metrics queries elsewhere, bare flag": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Args = append(d.Spec.Template.Spec.Containers[0].Args, "--metrics-tenancy-url", "https://evil.example")
			}},
			"the console sending metrics queries elsewhere by env": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, corev1.EnvVar{Name: "DOTVIRT_METRICS_URL", Value: "https://evil.example"})
			}},
			"the console with a kubeconfig flag": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Args = append(d.Spec.Template.Spec.Containers[0].Args, "-kubeconfig=/evil/kubeconfig")
			}},
			"the console serving other pages": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Args = append(d.Spec.Template.Spec.Containers[0].Args, "-static-dir=/evil")
			}},
			"the console through a proxy": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, corev1.EnvVar{Name: "HTTPS_PROXY", Value: "http://evil.example:3128"})
			}},
			"the console through a proxy, lowercase": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, corev1.EnvVar{Name: "https_proxy", Value: "http://evil.example:3128"})
			}},
			"the console trusting another store": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, corev1.EnvVar{Name: "SSL_CERT_FILE", Value: "/evil/ca.crt"})
			}},
			"the console trusting another OAuth CA": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, corev1.EnvVar{Name: "DOTVIRT_OAUTH_CA", Value: "/evil/ca.crt"})
			}},
			"the console trusting another OAuth CA by flag": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Args = append(d.Spec.Template.Spec.Containers[0].Args, "-oauth-ca=/evil/ca.crt")
			}},
			"the console trusting another origin": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Args = append(d.Spec.Template.Spec.Containers[0].Args, "-ui-origin=https://evil.example")
			}},
			"the console trusting another origin by env": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, corev1.EnvVar{Name: "DOTVIRT_UI_ORIGIN", Value: "https://evil.example"})
			}},
			// An env the console sends out, carrying a Secret it was never given.
			"the console sending another Secret as its forge token": {app, withEnv(
				fromSecret("DOTVIRT_FORGE_TOKEN", "admin-oidc-client", "clientSecret"),
				corev1.EnvVar{Name: "DOTVIRT_FORGE_URL", Value: "https://evil.example"})},
			"the console reading its own Secret under another env": {app, withEnv(fromSecret("DOTVIRT_GIT_USERNAME", adminobjects.OAuthSecret, "clientSecret"))},
			"the console reading another key of its own Secret":    {app, withEnv(fromSecret("DOTVIRT_SESSION_SECRET", adminobjects.SessionSecret, "other"))},
			// The admin brings the forge credential here (spec.forge.managed false).
			"the console sending an admin's forge credential elsewhere by env": {app, withEnv(corev1.EnvVar{Name: "DOTVIRT_FORGE_URL", Value: "https://evil.example"})},
			"the console sending an admin's forge credential elsewhere by flag": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Args = append(slices.Clone(c.Args), "-forge-url=https://evil.example")
			}},
			// The kubelet would put a Secret's value where $(NAME) stands.
			"the console's webhook URL carrying a Secret": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Args = append(slices.Clone(c.Args), "-webhook-url=https://evil.example/$(DOTVIRT_FORGE_CLIENT_SECRET)")
			}},
			"the console's upload proxy URL carrying a Secret": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Args = append(slices.Clone(c.Args), "-upload-proxy-url=https://evil.example/$(DOTVIRT_SESSION_SECRET)")
			}},
			"the console's forge web URL carrying a Secret": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Args = append(slices.Clone(c.Args), "-forge-web-url=https://evil.example/$(DOTVIRT_OIDC_CLIENT_SECRET)")
			}},
			"an env value carrying a Secret": {app, withEnv(corev1.EnvVar{Name: "DOTVIRT_PUBLIC_URL", Value: "https://evil.example/$(DOTVIRT_SESSION_SECRET)"})},
			// Not Delegated here: users' forge tokens are not the console's to take.
			"the console naming a forge application while not delegating":        {app, withEnv(corev1.EnvVar{Name: "DOTVIRT_FORGE_CLIENT_ID", Value: "dotvirt"})},
			"the console delegating to users' forge accounts no admin asked for": {app, withEnv(corev1.EnvVar{Name: "DOTVIRT_FORGE_DELEGATED", Value: "true"})},
			"the console delegating by flag": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Args = append(slices.Clone(c.Args), "-forge-delegated")
			}},
			"the console trusting another CA for its forge": {app, withEnv(corev1.EnvVar{Name: "DOTVIRT_FORGE_CA", Value: adminobjects.BrandingMountPath + "/ca.crt"})},
			// The console sends the admin's OIDC client secret to its issuer.
			"the console signing in at an issuer no admin chose": {app, withEnv(corev1.EnvVar{Name: "DOTVIRT_OIDC_ISSUER", Value: "https://evil.example"})},
			"the console signing in at an issuer no admin chose, by flag": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Args = append(slices.Clone(c.Args), "-oidc-issuer=https://evil.example")
			}},
			// The forge runs its own commands: a network volume reaches any
			// export the nodes can.
			"the forge on an NFS export": {forge, withVolume(corev1.Volume{Name: "more", VolumeSource: corev1.VolumeSource{
				NFS: &corev1.NFSVolumeSource{Server: "nfs.example", Path: "/exports"}}}, corev1.VolumeMount{Name: "more", MountPath: "/more"})},
			"the forge on an iSCSI target": {forge, withVolume(corev1.Volume{Name: "more", VolumeSource: corev1.VolumeSource{
				ISCSI: &corev1.ISCSIVolumeSource{TargetPortal: "203.0.113.7:3260", IQN: "iqn.2001-04.com.example:storage", Lun: 0}}}, corev1.VolumeMount{Name: "more", MountPath: "/more"})},
			"the forge with another ConfigMap":  {forge, withVolume(configMapVol("more", "brand"), corev1.VolumeMount{Name: "more", MountPath: "/more"})},
			"the forge on the console's drafts": {forge, withVolume(claimVol("data", adminobjects.DraftsClaim), corev1.VolumeMount{Name: "data", MountPath: "/var/lib/gitea"})},
			"the forge with another claim":      {forge, withVolume(claimVol("more", "other"), corev1.VolumeMount{Name: "more", MountPath: "/more"})},
			"the forge with an ephemeral claim": {forge, withVolume(corev1.Volume{Name: "more", VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{
				VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					DataSource:  &corev1.TypedLocalObjectReference{Kind: "PersistentVolumeClaim", Name: adminobjects.DraftsClaim},
					Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
				}}}}}, corev1.VolumeMount{Name: "more", MountPath: "/more"})},
			"the console with a ConfigMap over its binary": {app, withVolume(configMapVol("evil", "evil"),
				corev1.VolumeMount{Name: "evil", MountPath: "/dotvirt", SubPath: "dotvirt"})},
			"the console trusting the operator's certificates": {app, withVolume(configMapVol("evil", "evil"),
				corev1.VolumeMount{Name: "evil", MountPath: "/etc/ssl/certs"})},
			"the console branded from a ConfigMap no admin chose": {app, withVolume(configMapVol("branding", "evil"),
				corev1.VolumeMount{Name: "branding", MountPath: adminobjects.BrandingMountPath})},
			"the console's drafts on another claim": {app, withVolume(claimVol("drafts", "dotvirt-forgejo-data"),
				corev1.VolumeMount{Name: "drafts", MountPath: "/var/lib/dotvirt/drafts"})},
			"the console's drafts mounted over its trust store": {app, withVolume(claimVol("drafts", adminobjects.DraftsClaim),
				corev1.VolumeMount{Name: "drafts", MountPath: "/etc/ssl/certs"})},
			"the console mounting part of a volume": {app, withVolume(claimVol("drafts", adminobjects.DraftsClaim),
				corev1.VolumeMount{Name: "drafts", MountPath: "/var/lib/dotvirt/drafts", SubPath: "x"})},
			"the console's forge token from another Secret": {app, withVolume(secretVol("forge-token", adminobjects.ForgeAdminSecret),
				corev1.VolumeMount{Name: "forge-token", MountPath: adminobjects.ForgeMountPath})},
			"the forge reading another Secret": {forge, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, secretEnv(install.SessionSecretName))
			}},
			"the forge's bootstrap reading another Secret": {forge, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.InitContainers[0]
				c.Env = append(c.Env, secretEnv(install.OAuthSecretName))
			}},
			"the forge taking a Secret as env": {forge, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.EnvFrom = append(c.EnvFrom, corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: install.SessionSecretName}}})
			}},
			"the forge mounting a Secret": {forge, withVolume(secretVol("leak", install.SessionSecretName), corev1.VolumeMount{Name: "leak", MountPath: "/leak"})},
			"the forge mounting a projected Secret": {forge, withVolume(corev1.Volume{Name: "leak", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: install.SessionSecretName}}}}}}},
				corev1.VolumeMount{Name: "leak", MountPath: "/leak"})},
			"the console with an exec probe": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].LivenessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
					Exec: &corev1.ExecAction{Command: []string{"/dotvirt", "-addr=:9090", "-kubeconfig=/evil"}}}}
			}},
			"the console with an exec startup probe": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].StartupProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
					Exec: &corev1.ExecAction{Command: []string{"/dotvirt", "-addr=:9090"}}}}
			}},
			"the console with a postStart exec": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{PostStart: &corev1.LifecycleHandler{
					Exec: &corev1.ExecAction{Command: []string{"/dotvirt", "-addr=:9090"}}}}
			}},
			"the console with a preStop exec": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{PreStop: &corev1.LifecycleHandler{
					Exec: &corev1.ExecAction{Command: []string{"/dotvirt", "-addr=:9090"}}}}
			}},
			"the console sharing its process namespace": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.ShareProcessNamespace = new(true)
			}},
			"the forge's pods labeled as the console's": {forge, func(d *appsv1.Deployment) {
				// New maps: the render shares its selector map.
				d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "dotvirt"}}
				d.Spec.Template.Labels = map[string]string{"app": "dotvirt"}
			}},
			"the console resolving a host elsewhere": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.HostAliases = []corev1.HostAlias{{IP: "10.0.0.66", Hostnames: []string{"thanos-querier.openshift-monitoring.svc"}}}
			}},
			"the console with a resolver of its own": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.DNSPolicy = corev1.DNSNone
				d.Spec.Template.Spec.DNSConfig = &corev1.PodDNSConfig{Nameservers: []string{"10.0.0.66"}}
			}},
			"the console serving other pages by env": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, corev1.EnvVar{Name: "DOTVIRT_STATIC_DIR", Value: "/evil"})
			}},
			"the console with an init container": {app, func(d *appsv1.Deployment) {
				c := d.Spec.Template.Spec.Containers[0]
				c.Name, c.LivenessProbe, c.ReadinessProbe, c.Ports = "init", nil, nil, nil
				d.Spec.Template.Spec.InitContainers = []corev1.Container{c}
			}},

			"the console with a kubeconfig": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, corev1.EnvVar{Name: "KUBECONFIG", Value: "/evil/kubeconfig"})
			}},
			"the console with an apiserver address": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, corev1.EnvVar{Name: "KUBERNETES_SERVICE_HOST", Value: "10.0.0.66"})
			}},
			"the console with env from a ConfigMap": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "dotvirt-oauth"}}}}
			}},
			"the console without its token": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.AutomountServiceAccountToken = new(false)
			}},
			"a mount over the token directory": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "drafts", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount"})
			}},
			"a mount above the token directory": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "drafts", MountPath: "/run"})
			}},
			"a mount over the token directory, doubled slash": {app, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "drafts", MountPath: "/var/run//secrets/kubernetes.io/serviceaccount"})
			}},
			"a mount inside the token directory": {forge, func(d *appsv1.Deployment) {
				c := &d.Spec.Template.Spec.Containers[0]
				c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "etc", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"})
			}},
			"a container allowed root": {app, func(d *appsv1.Deployment) { sc(d).RunAsNonRoot = new(false) }},
			"a pod without seccomp":    {app, func(d *appsv1.Deployment) { d.Spec.Template.Spec.SecurityContext.SeccompProfile = nil }},
			"mounting a host path": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Volumes = append(d.Spec.Template.Spec.Volumes,
					corev1.Volume{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}})
			}},
			"in the host network": {app, func(d *appsv1.Deployment) { d.Spec.Template.Spec.HostNetwork = true }},
			"in the host PID":     {app, func(d *appsv1.Deployment) { d.Spec.Template.Spec.HostPID = true }},
			"in the host IPC":     {app, func(d *appsv1.Deployment) { d.Spec.Template.Spec.HostIPC = true }},
			"privileged": {app, func(d *appsv1.Deployment) {
				sc(d).Privileged, sc(d).AllowPrivilegeEscalation = new(true), nil
			}},
			"escalation left unset":            {app, func(d *appsv1.Deployment) { sc(d).AllowPrivilegeEscalation = nil }},
			"escalation allowed, unprivileged": {app, func(d *appsv1.Deployment) { sc(d).AllowPrivilegeEscalation = new(true) }},
			"adding a capability": {app, func(d *appsv1.Deployment) {
				sc(d).Capabilities.Add = []corev1.Capability{"NET_ADMIN"}
			}},
			"keeping capabilities": {app, func(d *appsv1.Deployment) { sc(d).Capabilities.Drop = nil }},
			"a container as root":  {app, func(d *appsv1.Deployment) { sc(d).RunAsUser = new(int64(0)) }},
			"a pod as root":        {app, func(d *appsv1.Deployment) { d.Spec.Template.Spec.SecurityContext.RunAsUser = new(int64(0)) }},
			"a container unconfined": {app, func(d *appsv1.Deployment) {
				sc(d).SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}
			}},
			"a pod unconfined": {app, func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.SecurityContext.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}
			}},
		} {
			d := c.d()
			c.edit(d)
			if err := dryApply(d); !refused(err) {
				t.Errorf("operator's Deployment %s: err=%v, want the guard's Forbidden", name, err)
			}
		}
		// The forge credential an admin brings, read from its Secret as rendered;
		// a managed forge's literal URL is not that.
		byo := dv.DeepCopy()
		byo.Spec.Forge.CredentialsSecret = adminobjects.DefaultForgeSecret
		managed := dv.DeepCopy()
		managed.Spec.Forge.Managed, managed.Spec.Forge.URL = true, "https://forge.example"
		if err := dryApply(install.Deployment(byo, "argocd", nil)); err != nil {
			t.Errorf("the console with an admin's forge credential: %v", err)
		}
		if err := dryApply(install.Deployment(managed, "argocd", nil)); !refused(err) {
			t.Errorf("a managed forge's URL while an admin brings the credential: err=%v, want the guard's Forbidden", err)
		}

		// Claims: as rendered, and bound to nothing of the operator's choosing.
		// Elsewhere they are new, so the apiserver's immutability does not
		// answer first.
		claim := func(namespace string, edit func(*corev1.PersistentVolumeClaimSpec)) *corev1.PersistentVolumeClaim {
			c := install.DraftsPVC(dv)
			c.Namespace = namespace
			if edit != nil {
				edit(&c.Spec)
			}
			return c
		}
		for name, o := range map[string]client.Object{
			"the drafts claim": claim(dv.Namespace, nil), "a new drafts claim": claim("team-a", nil), "the forge's claim": install.ForgejoPVC(dv),
		} {
			if err := dryApply(o); err != nil {
				t.Errorf("%s as rendered: %v", name, err)
			}
		}
		for name, o := range map[string]client.Object{
			"the drafts claim bound to a volume of its choosing": claim(dv.Namespace, func(c *corev1.PersistentVolumeClaimSpec) { c.VolumeName = "pv-evil" }),
			"a new claim bound to a volume":                      claim("team-a", func(c *corev1.PersistentVolumeClaimSpec) { c.VolumeName = "pv-evil" }),
			"a new claim selecting volumes": claim("team-a", func(c *corev1.PersistentVolumeClaimSpec) {
				c.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"tenant": "b"}}
			}),
			"a new claim copying another": claim("team-a", func(c *corev1.PersistentVolumeClaimSpec) {
				c.DataSource = &corev1.TypedLocalObjectReference{Kind: "PersistentVolumeClaim", Name: adminobjects.ForgeClaim}
			}),
			"a new claim populated from another": claim("team-a", func(c *corev1.PersistentVolumeClaimSpec) {
				c.DataSourceRef = &corev1.TypedObjectReference{Kind: "PersistentVolumeClaim", Name: adminobjects.ForgeClaim}
			}),
		} {
			if err := dryApply(o); !refused(err) {
				t.Errorf("operator's %s: err=%v, want the guard's Forbidden", name, err)
			}
		}

		// Bindings: every one the operator renders passes; its own account, an
		// unrendered name or role, or another namespace is refused.
		for _, b := range []client.Object{
			install.DotvirtClusterRoleBinding(dv),
			install.ArgocdApplyClusterRoleBinding(dv, "argocd", "argocd-application-controller", true, true),
			install.ArgocdReadClusterRoleBinding(dv, "argocd", "argocd-application-controller"),
			install.PlatformSyncClusterRoleBinding(dv, "argocd"),
			install.PlatformNetworkAdminBinding(dv),
			install.StorageCatalogBinding(dv),
			install.SyncRequestRoleBinding(dv, "argocd"),
		} {
			if err := dryApply(b); err != nil {
				t.Errorf("rendered %s: %v", b.GetName(), err)
			}
		}
		self := rbacv1.Subject{Kind: "ServiceAccount", Name: "dotvirt-operator", Namespace: "dotvirt-operator"}
		for name, b := range map[string]client.Object{
			"dotvirt-argocd-apply to the operator": func() client.Object {
				b := install.ArgocdApplyClusterRoleBinding(dv, "argocd", "argocd-application-controller", true, true)
				b.Subjects = append(b.Subjects, self)
				return b
			}(),
			"dotvirt-platform-sync to the operator": func() client.Object {
				b := install.PlatformSyncClusterRoleBinding(dv, "argocd")
				b.Subjects = []rbacv1.Subject{self}
				return b
			}(),
			// Not created in this install (no platform repo), so its roleRef is free.
			"another role under a rendered name": func() client.Object {
				b := install.PlatformSyncClusterRoleBinding(dv, "argocd")
				b.RoleRef.Name = "dotvirt-argocd-apply"
				return b
			}(),
			"the sync-request binding elsewhere": install.SyncRequestRoleBinding(dv, "team-a"),
			"the sync-request binding to the operator": func() client.Object {
				b := install.SyncRequestRoleBinding(dv, "argocd")
				b.Subjects = append(b.Subjects, self)
				return b
			}(),
		} {
			if err := dryApply(b); !refused(err) {
				t.Errorf("operator's binding %s: err=%v, want the guard's Forbidden", name, err)
			}
		}

		// The console's front door: its Service and exposure as rendered pass;
		// re-pointed at anything else, refused.
		svc := func(edit func(*corev1.Service)) *corev1.Service {
			s := install.Service(dv)
			if edit != nil {
				edit(s)
			}
			return s
		}
		route := func(name, host string, edit func(map[string]any)) *unstructured.Unstructured {
			r := install.Route(dv, name, host)
			if edit != nil {
				edit(r.Object["spec"].(map[string]any))
			}
			return r
		}
		generated := func(extra map[string]string) *unstructured.Unstructured {
			r := route(install.AppName, "", nil)
			a := map[string]string{"openshift.io/host.generated": "true"}
			maps.Copy(a, extra)
			r.SetAnnotations(a)
			return r
		}
		forgeSvc := func(namespace string, edit func(*corev1.ServiceSpec)) *corev1.Service {
			s := install.ForgejoService(dv)
			s.Namespace = namespace
			edit(&s.Spec)
			return s
		}
		account := func(automount bool) *corev1.ServiceAccount {
			sa := install.ServiceAccount(dv)
			sa.AutomountServiceAccountToken = &automount
			return sa
		}
		for name, o := range map[string]client.Object{
			"the console's account":         install.ServiceAccount(dv),
			"the console's account mounted": account(true),
			"the forge's account":           install.ForgejoServiceAccount(dv),
			// OpenShift's route API marks the host it generates, on create,
			// before admission.
			"the console's Route with OpenShift's generated-host mark": generated(nil),
			"the console's Service": svc(nil),
			"the console's Route":   route(install.AppName, "", nil),
			// The host the router gives a hostless Route, while the guard
			// knows none (no cluster ingress domain here).
			"the console's Route with the router's host": route(install.AppName, "dotvirt-dotvirt.apps.example", nil),
			"the forge's Service":                        install.ForgejoService(dv),
			"the forge's Route":                          route(install.ForgejoServiceName, "", nil),
		} {
			if err := dryApply(o); err != nil {
				t.Errorf("%s as rendered: %v", name, err)
			}
		}
		for name, o := range map[string]client.Object{
			"the console's Service selecting the forge": svc(func(s *corev1.Service) { s.Spec.Selector = map[string]string{"app": "dotvirt-forgejo"} }),
			"the console's Service on another port": svc(func(s *corev1.Service) {
				s.Spec.Ports[0].TargetPort = intstr.FromInt32(3000)
			}),
			"the console's Service exposed on nodes":        svc(func(s *corev1.Service) { s.Spec.Type = corev1.ServiceTypeNodePort }),
			"the console's Service publishing unready pods": svc(func(s *corev1.Service) { s.Spec.PublishNotReadyAddresses = true }),
			"the console's Route to the forge": route(install.AppName, "", func(spec map[string]any) {
				spec["to"] = map[string]any{"kind": "Service", "name": install.ForgejoServiceName}
			}),
			"the console's Route splitting to the forge": route(install.AppName, "", func(spec map[string]any) {
				spec["alternateBackends"] = []any{map[string]any{"kind": "Service", "name": install.ForgejoServiceName, "weight": int64(50)}}
			}),
			"the console's Route passed through": route(install.AppName, "", func(spec map[string]any) {
				spec["tls"] = map[string]any{"termination": "passthrough"}
			}),
			"the console's account without its token":               account(false),
			"the forge's Service on node ports":                     forgeSvc(dv.Namespace, func(s *corev1.ServiceSpec) { s.Type = corev1.ServiceTypeNodePort }),
			"the forge's Service on an external IP":                 forgeSvc(dv.Namespace, func(s *corev1.ServiceSpec) { s.ExternalIPs = []string{"203.0.113.7"} }),
			"a forge Service elsewhere as a load balancer":          forgeSvc("team-a", func(s *corev1.ServiceSpec) { s.Type = corev1.ServiceTypeLoadBalancer }),
			"the console's Route with OpenShift's mark and another": generated(map[string]string{"haproxy.router.openshift.io/rewrite-target": "/evil"}),
			"the console's Route annotated": func() client.Object {
				r := route(install.AppName, "", nil)
				r.SetAnnotations(map[string]string{"haproxy.router.openshift.io/rewrite-target": "/evil"})
				return r
			}(),
		} {
			if err := dryApply(o); !refused(err) {
				t.Errorf("operator's %s: err=%v, want the guard's Forbidden", name, err)
			}
		}

		// An unrendered name: the operator's role names its bindings, so grant
		// it this one too, leaving the guard's name rule the only gate.
		// The binding grants nothing and names no subject: only its name is wrong.
		mustCreate(t, admin, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "dotvirt-evil"}})
		mustCreate(t, admin, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "guard-probe-evil"},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{rbacv1.GroupName}, Resources: []string{"clusterrolebindings"}, ResourceNames: []string{"dotvirt-evil"}, Verbs: []string{"create", "patch"}},
				{APIGroups: []string{rbacv1.GroupName}, Resources: []string{"clusterroles"}, ResourceNames: []string{"dotvirt-evil"}, Verbs: []string{"bind"}},
			},
		})
		mustCreate(t, admin, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "guard-probe-evil"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "guard-probe-evil"},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: operatorUser}},
		})
		evil := install.ArgocdApplyClusterRoleBinding(dv, "argocd", "argocd-application-controller", true, true)
		evil.Name, evil.RoleRef.Name, evil.Subjects = "dotvirt-evil", "dotvirt-evil", nil
		// Until RBAC sees the grant, only it answers; then the guard does.
		eventually(t, "the probe grant", func() bool {
			err := dryApply(evil)
			return err == nil || (!strings.Contains(err.Error(), "cannot patch resource") && !strings.Contains(err.Error(), "cannot create resource"))
		})
		if err := dryApply(evil); !refused(err) {
			t.Errorf("operator's binding under an unrendered name: err=%v, want the guard's Forbidden", err)
		}
		_ = admin.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "guard-probe-evil"}})
		_ = admin.Delete(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "guard-probe-evil"}})
		_ = admin.Delete(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "dotvirt-evil"}})

		// Its install: the finalizers go on and off by the operator's own
		// patch; the spec, labels and owners an admin wrote stay theirs.
		// Status is another resource and passes.
		opPatch := func(edit func(*dotvirtv1alpha1.Dotvirt)) error {
			got := &dotvirtv1alpha1.Dotvirt{}
			if err := opClient.Get(ctx, key, got); err != nil {
				t.Fatal(err)
			}
			lock := client.MergeFromWithOptions(got.DeepCopy(), client.MergeFromWithOptimisticLock{})
			edit(got)
			return opClient.Patch(ctx, got, lock)
		}
		for name, edit := range map[string]func(*dotvirtv1alpha1.Dotvirt){
			"spec.image": func(d *dotvirtv1alpha1.Dotvirt) { d.Spec.Image = "registry.example/evil/dotvirt:1" },
			"spec.forge.credentialsSecret": func(d *dotvirtv1alpha1.Dotvirt) {
				d.Spec.Forge.CredentialsSecret = "kube-admin-token"
			},
			"a label":       func(d *dotvirtv1alpha1.Dotvirt) { d.Labels = map[string]string{"evil": "1"} },
			"an annotation": func(d *dotvirtv1alpha1.Dotvirt) { d.Annotations = map[string]string{"evil": "1"} },
		} {
			if err := opPatch(edit); !refused(err) {
				t.Errorf("operator changing its install's %s: err=%v, want the guard's Forbidden", name, err)
			}
		}
		statusPatch := func() error {
			got := &dotvirtv1alpha1.Dotvirt{}
			if err := opClient.Get(ctx, key, got); err != nil {
				t.Fatal(err)
			}
			base := got.DeepCopy()
			got.Status.ConsoleURL = "https://probe.example"
			return opClient.Status().Patch(ctx, got, client.MergeFrom(base))
		}
		if err := statusPatch(); err != nil {
			t.Errorf("operator's status write: %v", err)
		}
		// Off by the operator's patch, and back on by its own pass under the guard.
		if err := opPatch(func(d *dotvirtv1alpha1.Dotvirt) { controllerutil.RemoveFinalizer(d, dotvirtFinalizer) }); err != nil {
			t.Errorf("operator removing its finalizer: %v", err)
		}
		patchCR()
		eventually(t, "the finalizer back by the operator's pass", func() bool {
			got := &dotvirtv1alpha1.Dotvirt{}
			return admin.Get(ctx, key, got) == nil && controllerutil.ContainsFinalizer(got, dotvirtFinalizer)
		})

		// An image an admin chose passes once the guard is installed for it.
		custom := app()
		custom.Spec.Template.Spec.Containers[0].Image = "registry.example/team/dotvirt:custom"
		if err := dryApply(custom); !refused(err) {
			t.Errorf("an image no admin chose: err=%v, want the guard's Forbidden", err)
		}
		chosen := pins
		chosen.SpecImage = custom.Spec.Template.Spec.Containers[0].Image
		// Where an admin sends metrics queries: as the operator renders it.
		chosen.Metrics = adminobjects.MetricsFor("https://thanos-querier.openshift-monitoring.svc:9091", "", false)
		// The console's host, which nothing else may claim.
		chosen.ConsoleHost = "console.example"
		ingress := func(name string, edit func(*networkingv1.Ingress)) *networkingv1.Ingress {
			port := install.HTTPPort
			if name == install.ForgejoServiceName {
				port = 3000
			}
			i := install.Ingress(dv, name, port, chosen.ConsoleHost)
			if edit != nil {
				edit(i)
			}
			return i
		}
		httpRoute := func(name string, edit func(map[string]any)) *unstructured.Unstructured {
			hd := dv.DeepCopy()
			hd.Spec.Ingress.Gateway = &dotvirtv1alpha1.GatewayRef{Name: "public", Namespace: "gateways"}
			r := install.HTTPRoute(hd, name, install.HTTPPort, chosen.ConsoleHost)
			if edit != nil {
				edit(r.Object["spec"].(map[string]any))
			}
			return r
		}
		metrics := app()
		mc := &metrics.Spec.Template.Spec.Containers[0]
		mc.Env = append(mc.Env,
			corev1.EnvVar{Name: "DOTVIRT_METRICS_URL", Value: chosen.Metrics.URL},
			corev1.EnvVar{Name: "DOTVIRT_METRICS_TENANCY_URL", Value: chosen.Metrics.TenancyURL},
			corev1.EnvVar{Name: "DOTVIRT_METRICS_CA", Value: chosen.Metrics.CA})
		elsewhere := metrics.DeepCopy()
		elsewhere.Spec.Template.Spec.Containers[0].Args = append(elsewhere.Spec.Template.Spec.Containers[0].Args, "-metrics-url=https://evil.example")
		// The branding ConfigMap and the OIDC client secret an admin chose: the
		// secret reaches the console under its own env only.
		chosen.BrandingConfigMap = "brand"
		chosen.OIDCSecret, chosen.OIDCSecretKey, chosen.OIDCIssuer = "admin-oidc", "client-secret", "https://idp.example"
		// Delegated pull requests through the forge application an admin
		// registered: users' tokens and its client secret go only where the
		// spec says.
		chosen.ForgeDelegated, chosen.ForgeURL, chosen.ForgeClientID = true, "https://forge.example", "dotvirt"
		chosen.ForgeAuthorizeURL, chosen.ForgeClientSecret, chosen.ForgeClientSecretKey = "https://forge.example/login/oauth/authorize", "forge-app", "client-secret"
		// The console as the operator renders it for that spec.
		delegatedDV := dv.DeepCopy()
		delegatedDV.Spec.Forge.CredentialsSecret, delegatedDV.Spec.Forge.URL = adminobjects.DefaultForgeSecret, chosen.ForgeURL
		delegatedDV.Spec.Forge.PullRequests = "Delegated"
		delegatedDV.Spec.Forge.UserAuth = &dotvirtv1alpha1.ForgeUserAuthSpec{ClientID: chosen.ForgeClientID, AuthorizeURL: chosen.ForgeAuthorizeURL,
			ClientSecret: dotvirtv1alpha1.SecretKeyRef{Name: chosen.ForgeClientSecret, Key: chosen.ForgeClientSecretKey}}
		delegated := install.Deployment(delegatedDV, "argocd", nil)
		delegatedWith := func(env ...corev1.EnvVar) *appsv1.Deployment {
			d := delegated.DeepCopy()
			withEnv(env...)(d)
			return d
		}
		oidc := app()
		withEnv(fromSecret("DOTVIRT_OIDC_CLIENT_SECRET", "admin-oidc", "client-secret"),
			corev1.EnvVar{Name: "DOTVIRT_OIDC_ISSUER", Value: chosen.OIDCIssuer})(oidc)
		oidcIssuerElsewhere := oidc.DeepCopy()
		withEnv(corev1.EnvVar{Name: "DOTVIRT_OIDC_ISSUER", Value: "https://evil.example"})(oidcIssuerElsewhere)
		oidcElsewhere := app()
		withEnv(fromSecret("DOTVIRT_FORGE_TOKEN", "admin-oidc", "client-secret"),
			fromSecret("DOTVIRT_FORGE_URL", adminobjects.DefaultForgeSecret, "url"))(oidcElsewhere)
		branded := app()
		withVolume(configMapVol("branding", "brand"), corev1.VolumeMount{Name: "branding", MountPath: adminobjects.BrandingMountPath})(branded)
		forgeRoute := func(hostnames ...any) *unstructured.Unstructured {
			return httpRoute(install.ForgejoServiceName, func(spec map[string]any) {
				delete(spec, "hostnames")
				if hostnames != nil {
					spec["hostnames"] = hostnames
				}
				spec["rules"] = []any{map[string]any{"backendRefs": []any{map[string]any{"name": install.ForgejoServiceName, "port": int64(3000)}}}}
			})
		}
		forgeIngress := func(host string, edit func(*networkingv1.Ingress)) *networkingv1.Ingress {
			return ingress(install.ForgejoServiceName, func(i *networkingv1.Ingress) {
				i.Spec.Rules[0].Host = host
				if edit != nil {
					edit(i)
				}
			})
		}
		installGuard(chosen)
		eventually(t, "the admin's image admitted", func() bool { return dryApply(custom) == nil })
		if err := dryApply(metrics); err != nil {
			t.Errorf("the admin's metrics endpoints: %v", err)
		}
		if err := dryApply(elsewhere); !refused(err) {
			t.Errorf("metrics queries beside the admin's choice: err=%v, want the guard's Forbidden", err)
		}
		for name, o := range map[string]client.Object{
			"the console branded from the admin's ConfigMap":               branded,
			"the console with the admin's OIDC client secret":              oidc,
			"the console delegating through the admin's forge application": delegated,
			"the forge's Ingress on its own host":                          forgeIngress("forge.example", nil),
			"the forge's HTTPRoute on its own host":                        forgeRoute("forge.example"),
			"the console's Route on its host":                              route(install.AppName, chosen.ConsoleHost, nil),
			"the console's Ingress on its host":                            ingress(install.AppName, nil),
			"the console's HTTPRoute on its host":                          httpRoute(install.AppName, nil),
		} {
			if err := dryApply(o); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
		for name, o := range map[string]client.Object{
			"the console sending users' forge tokens to another forge":                     delegatedWith(corev1.EnvVar{Name: "DOTVIRT_FORGE_DELEGATED_URL", Value: "https://evil.example"}),
			"the console sending the forge application's secret to another token endpoint": delegatedWith(corev1.EnvVar{Name: "DOTVIRT_FORGE_TOKEN_URL", Value: "https://evil.example/token"}),
			"the console sending users to another authorize page":                          delegatedWith(corev1.EnvVar{Name: "DOTVIRT_FORGE_AUTHORIZE_URL", Value: "https://evil.example/authorize"}),
			"the console acting as another forge application":                              delegatedWith(corev1.EnvVar{Name: "DOTVIRT_FORGE_CLIENT_ID", Value: "other"}),
			"the console with a literal forge client secret":                               delegatedWith(corev1.EnvVar{Name: "DOTVIRT_FORGE_CLIENT_SECRET", Value: "chosen-by-the-operator"}),
			"the console with the forge client secret from another Secret":                 delegatedWith(fromSecret("DOTVIRT_FORGE_CLIENT_SECRET", adminobjects.SessionSecret, "secret")),
			"the console with a forge client secret by flag": func() client.Object {
				d := delegated.DeepCopy()
				d.Spec.Template.Spec.Containers[0].Args = append(slices.Clone(d.Spec.Template.Spec.Containers[0].Args), "-forge-client-secret=x")
				return d
			}(),
			"the console sending the admin's OIDC client secret to another issuer":  oidcIssuerElsewhere,
			"the console sending the admin's OIDC client secret as its forge token": oidcElsewhere,
			"the forge's Ingress for every host":                                    forgeIngress("", nil),
			"the forge's Ingress on a wildcard over the console's host":             forgeIngress("*.example", nil),
			"the forge's Ingress with a default backend": forgeIngress("forge.example", func(i *networkingv1.Ingress) {
				i.Spec.DefaultBackend = i.Spec.Rules[0].HTTP.Paths[0].Backend.DeepCopy()
			}),
			"the forge's HTTPRoute for every host":                        forgeRoute(),
			"the forge's HTTPRoute on a wildcard over the console's host": forgeRoute("*.example"),
			"the forge's Route claiming the console's host":               route(install.ForgejoServiceName, chosen.ConsoleHost, nil),
			"the forge's Ingress claiming the console's host":             ingress(install.ForgejoServiceName, nil),
			"the forge's HTTPRoute claiming the console's host": func() client.Object {
				r := httpRoute(install.ForgejoServiceName, nil)
				r.Object["spec"].(map[string]any)["rules"] = []any{map[string]any{"backendRefs": []any{map[string]any{"name": install.ForgejoServiceName, "port": int64(3000)}}}}
				return r
			}(),
			"the forge's Route on a wildcard":     route(install.ForgejoServiceName, "x.example", func(spec map[string]any) { spec["wildcardPolicy"] = "Subdomain" }),
			"the console's Route on another host": route(install.AppName, "evil.example", nil),
			"the console's Ingress to the forge": ingress(install.AppName, func(i *networkingv1.Ingress) {
				i.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = install.ForgejoServiceName
			}),
			"the console's Ingress on another host": ingress(install.AppName, func(i *networkingv1.Ingress) { i.Spec.Rules[0].Host = "evil.example" }),
			"the console's Ingress mirrored": ingress(install.AppName, func(i *networkingv1.Ingress) {
				i.Annotations = map[string]string{"nginx.ingress.kubernetes.io/mirror-target": "https://evil.example$request_uri"}
			}),
			"the console's HTTPRoute to the forge": httpRoute(install.AppName, func(spec map[string]any) {
				spec["rules"] = []any{map[string]any{"backendRefs": []any{map[string]any{"name": install.ForgejoServiceName, "port": int64(3000)}}}}
			}),
			"the console's HTTPRoute mirrored": httpRoute(install.AppName, func(spec map[string]any) {
				spec["rules"] = []any{map[string]any{
					"filters":     []any{map[string]any{"type": "RequestMirror", "requestMirror": map[string]any{"backendRef": map[string]any{"name": install.ForgejoServiceName, "port": int64(3000)}}}},
					"backendRefs": []any{map[string]any{"name": install.AppName, "port": int64(install.HTTPPort)}},
				}}
			}),
		} {
			if err := dryApply(o); !refused(err) {
				t.Errorf("operator's %s: err=%v, want the guard's Forbidden", name, err)
			}
		}
		installGuard(pins)

		// The operator can neither re-apply nor remove its guard.
		apply := admissionregistrationv1ac.ValidatingAdmissionPolicy(adminobjects.GuardSecretsName).
			WithSpec(admissionregistrationv1ac.ValidatingAdmissionPolicySpec().WithValidations())
		if _, err := op.AdmissionregistrationV1().ValidatingAdmissionPolicies().Apply(ctx, apply, metav1.ApplyOptions{FieldManager: operatorFieldManager, Force: true}); !apierrors.IsForbidden(err) {
			t.Errorf("operator re-applying the guard: err=%v, want Forbidden", err)
		}
		if err := op.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Delete(ctx, adminobjects.GuardDeploymentsName, metav1.DeleteOptions{}); !apierrors.IsForbidden(err) {
			t.Errorf("operator deleting the guard's binding: err=%v, want Forbidden", err)
		}
		// With the guard in place, a pass still reconciles the install.
		eventually(t, "the custom image refused again", func() bool { return refused(dryApply(custom)) })
		patchCR()
		eventually(t, "a pass under the guard", func() bool {
			got := &dotvirtv1alpha1.Dotvirt{}
			if admin.Get(ctx, key, got) != nil {
				return false
			}
			w := apimeta.FindStatusCondition(got.Status.Conditions, dotvirtv1alpha1.ConditionWorkloadReady)
			return got.Status.ObservedGeneration == got.Generation && w != nil && w.Status == metav1.ConditionTrue
		})

	})

	// A release whose operand the previous release's guard refuses breaks every
	// upgrade until an admin re-installs the guard, while the old console reads
	// Installed. The baseline is the previous release's install/guard.yaml,
	// refreshed from the released asset at each release; a release that cannot
	// pass it ships with a re-install note.
	t.Run("the operand passes the previous release's guard", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("testdata", "guard-previous.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		filled := string(raw)
		for k, v := range map[string]string{
			"${OPERATOR_USERNAME}": operatorUser, "${INSTALL_NAMESPACE}": "dotvirt", "${ARGO_NAMESPACE}": "argocd",
			"${ARGO_CONTROLLER}": "argocd-application-controller", "${SPEC_IMAGE}": "", "${FORGE_INSECURE_TLS}": "false",
			"${RELATED_IMAGE_DOTVIRT}": "", "${RELATED_IMAGE_FORGEJO}": "", "${METRICS_URL}": "", "${METRICS_TENANCY_URL}": "", "${METRICS_CA}": "",
			"${CONSOLE_HOST}": "", "${BRANDING_CONFIGMAP}": "", "${METRICS_CA_CONFIGMAP}": "", "${FORGE_SECRET}": adminobjects.DefaultForgeSecret,
			"${FORGE_MANAGED}": "false", "${OIDC_SECRET}": "", "${OIDC_SECRET_KEY}": "", "${OIDC_ISSUER}": "",
			"${FORGE_DELEGATED}": "false", "${FORGE_URL}": "", "${FORGE_CLIENT_ID}": "", "${FORGE_AUTHORIZE_URL}": "", "${FORGE_TOKEN_URL}": "",
			"${FORGE_CLIENT_SECRET}": "", "${FORGE_CLIENT_SECRET_KEY}": "",
		} {
			filled = strings.ReplaceAll(filled, k, v)
		}
		var objs []*unstructured.Unstructured
		for _, doc := range strings.Split(filled, "\n---\n")[1:] {
			o := &unstructured.Unstructured{}
			if err := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(doc), 4096).Decode(&o.Object); err != nil {
				t.Fatal(err)
			}
			if err := admin.Apply(ctx, client.ApplyConfigurationFromUnstructured(o.DeepCopy()), client.FieldOwner("dotvirt"), client.ForceOwnership); err != nil {
				t.Fatalf("install the previous guard's %s: %v", o.GetName(), err)
			}
			objs = append(objs, o)
		}
		opClient, err := client.New(opCfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		dryApply := func(o client.Object) error {
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
			if err != nil {
				t.Fatal(err)
			}
			return opClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(&unstructured.Unstructured{Object: m}),
				client.FieldOwner(operatorFieldManager), client.ForceOwnership, client.DryRunAll)
		}
		t.Cleanup(func() {
			for _, o := range objs {
				_ = admin.Delete(context.Background(), o)
			}
			probe := &corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
				ObjectMeta: metav1.ObjectMeta{Name: install.OAuthSecretName, Namespace: "team-a"}}
			eventually(t, "the previous guard lifted", func() bool { return dryApply(probe) == nil })
		})
		token := &corev1.Secret{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: install.OAuthSecretName, Namespace: "dotvirt",
				Annotations: map[string]string{corev1.ServiceAccountNameKey: "default"}},
			Type: corev1.SecretTypeServiceAccountToken,
		}
		eventually(t, "the previous guard enforced", func() bool {
			err := dryApply(token)
			return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "dotvirt operator guard")
		})
		dv := &dotvirtv1alpha1.Dotvirt{}
		if err := admin.Get(ctx, types.NamespacedName{Namespace: "dotvirt", Name: "x"}, dv); err != nil {
			t.Fatal(err)
		}
		for _, o := range []client.Object{
			install.Deployment(dv, "argocd", nil),
			install.ForgejoDeployment(dv, false, "", ""),
			install.DotvirtClusterRoleBinding(dv),
			install.ArgocdApplyClusterRoleBinding(dv, "argocd", "argocd-application-controller", true, true),
			install.ArgocdReadClusterRoleBinding(dv, "argocd", "argocd-application-controller"),
			install.PlatformSyncClusterRoleBinding(dv, "argocd"),
			install.PlatformNetworkAdminBinding(dv),
			install.StorageCatalogBinding(dv),
			install.SyncRequestRoleBinding(dv, "argocd"),
		} {
			if err := dryApply(o); err != nil {
				t.Errorf("the previous guard refuses this release's %s %s: %v", o.GetObjectKind().GroupVersionKind().Kind, o.GetName(), err)
			}
		}
	})

	t.Run("a labeled namespace confines its project", func(t *testing.T) {
		mustCreate(t, admin, projectNS("team-b", "team-b", "https://forge.example/dotvirt/team-b.git"))
		ap := argoObj("AppProject", "", "", nil)
		eventually(t, "team-b AppProject", func() bool {
			return admin.Get(ctx, types.NamespacedName{Namespace: "argocd", Name: "dotvirt-p-team-b"}, ap) == nil
		})
		dests, _, _ := unstructured.NestedSlice(ap.Object, "spec", "destinations")
		if len(dests) != 1 || dests[0].(map[string]any)["namespace"] != "team-b" {
			t.Errorf("destinations = %v, want team-b only", dests)
		}
	})

	t.Run("label removal and relabel re-derive the AppProjects", func(t *testing.T) {
		dests := func(name string) ([]string, bool) {
			ap := argoObj("AppProject", "", "", nil)
			if admin.Get(ctx, types.NamespacedName{Namespace: "argocd", Name: name}, ap) != nil {
				return nil, false
			}
			ds, _, _ := unstructured.NestedSlice(ap.Object, "spec", "destinations")
			var out []string
			for _, d := range ds {
				out = append(out, d.(map[string]any)["namespace"].(string))
			}
			return out, true
		}
		mustCreate(t, admin, projectNS("team-b2", "team-b", "https://forge.example/dotvirt/team-b.git"))
		eventually(t, "team-b gains team-b2", func() bool { d, _ := dests("dotvirt-p-team-b"); return len(d) == 2 })
		ns := &corev1.Namespace{}
		if err := admin.Get(ctx, types.NamespacedName{Name: "team-b2"}, ns); err != nil {
			t.Fatal(err)
		}
		delete(ns.Labels, install.ProjectLabel)
		if err := admin.Update(ctx, ns); err != nil {
			t.Fatal(err)
		}
		// The label leaving takes the namespace out of the cache: a delete event.
		eventually(t, "team-b loses team-b2", func() bool { d, _ := dests("dotvirt-p-team-b"); return len(d) == 1 && d[0] == "team-b" })
		if err := admin.Get(ctx, types.NamespacedName{Name: "team-b"}, ns); err != nil {
			t.Fatal(err)
		}
		ns.Labels[install.ProjectLabel] = "team-z"
		if err := admin.Update(ctx, ns); err != nil {
			t.Fatal(err)
		}
		eventually(t, "team-z derived", func() bool { d, ok := dests("dotvirt-p-team-z"); return ok && len(d) == 1 && d[0] == "team-b" })
		eventually(t, "unreferenced team-b pruned", func() bool { _, ok := dests("dotvirt-p-team-b"); return !ok })
	})

	t.Run("admission refuses a Gateway exposure nothing could render", func(t *testing.T) {
		gw := &dotvirtv1alpha1.GatewayRef{Name: "edge", Namespace: "gateway-system"}
		for name, ingress := range map[string]dotvirtv1alpha1.IngressSpec{
			"type gateway, no Gateway": {Type: dotvirtv1alpha1.IngressGateway, Host: "h.example.com"},
			"a Gateway, no host":       {Gateway: gw},
		} {
			dv := testCR()
			dv.Spec.Ingress = ingress
			if err := admin.Create(ctx, dv, client.DryRunAll); !apierrors.IsInvalid(err) {
				t.Errorf("%s: got %v, want Invalid", name, err)
			}
		}
		// Unstructured: the typed client drops an empty host before it is sent.
		empty := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt",
			"metadata": map[string]any{"name": "empty-host", "namespace": "dotvirt"},
			"spec":     map[string]any{"ingress": map[string]any{"host": "", "gateway": map[string]any{"name": "edge"}}},
		}}
		if err := admin.Create(ctx, empty, client.DryRunAll); !apierrors.IsInvalid(err) {
			t.Errorf("a Gateway with an empty host: got %v, want Invalid", err)
		}
		dv := testCR()
		dv.Spec.Ingress = dotvirtv1alpha1.IngressSpec{Host: "h.example.com", Gateway: gw}
		if err := admin.Create(ctx, dv, client.DryRunAll); client.IgnoreAlreadyExists(err) != nil {
			t.Errorf("a named Gateway with a host: %v", err)
		}
	})

	t.Run("admission refuses a forge web base the app refuses", func(t *testing.T) {
		// The app's own check (internal/config webBase) refuses each of these
		// at startup: admission refusing them first keeps dotvirt from a
		// crashloop.
		for _, u := range []string{
			"javascript:alert(1)", "/forge", "c.example/forge", "ftp://c.example", "https:///forge",
			"https://?x", "https://@", "https://host:abc", "https://u:p@host", "https://host?x", "https://host?", "https://host#f", "https://host/a b",
			"https://host/%zz", "https://host/a%2", "https://host/a\x7fb", "https://host/a\x01b",
		} {
			dv := testCR()
			dv.Spec.Forge.WebURL = u
			if err := admin.Create(ctx, dv, client.DryRunAll); !apierrors.IsInvalid(err) {
				t.Errorf("webURL %q: got %v, want Invalid", u, err)
			}
		}
		for _, u := range []string{"https://c.example", "https://c.example/forge/", "https://c.example/a%2Fb", "http://forge.dotvirt.svc:3000"} {
			dv := testCR()
			dv.Spec.Forge.WebURL = u
			if err := admin.Create(ctx, dv, client.DryRunAll); client.IgnoreAlreadyExists(err) != nil {
				t.Errorf("webURL %q: %v", u, err)
			}
		}
	})

	t.Run("admission takes metrics endpoints the console can query", func(t *testing.T) {
		ca := &dotvirtv1alpha1.ConfigMapKeyRef{Name: "metrics-ca", Key: "ca.crt"}
		const wide, tenancy = "https://prom-wide.monitoring.svc:8443", "https://prom-tenancy.monitoring.svc:8443"
		for name, m := range map[string]dotvirtv1alpha1.MetricsSpec{
			"tenancyURL without url":         {TenancyURL: tenancy},
			"ca without url":                 {CA: ca},
			"tenancyURL not http":            {URL: wide, TenancyURL: "prom-tenancy.monitoring.svc:8443"},
			"tenancyURL with a query":        {URL: wide, TenancyURL: tenancy + "?x=1"},
			"tenancyURL with an empty query": {URL: wide, TenancyURL: tenancy + "/?"},
			"tenancyURL with a fragment":     {URL: wide, TenancyURL: tenancy + "#f"},
			"tenancyURL with credentials":    {URL: wide, TenancyURL: "https://u:p@prom-tenancy.monitoring.svc:8443"},
			"tenancyURL equal to url":        {URL: wide, TenancyURL: wide},
			"tenancyURL equal to url plus /": {URL: wide, TenancyURL: wide + "/"},
			"url equal to tenancyURL plus /": {URL: wide + "/", TenancyURL: wide},
			"ca with only http endpoints":    {URL: "http://prom.monitoring.svc:9090", TenancyURL: "http://proxy.monitoring.svc:8080", CA: ca},
			"ca with a slash in its key":     {URL: wide, CA: &dotvirtv1alpha1.ConfigMapKeyRef{Name: "metrics-ca", Key: "a/b"}},
			"ca key .":                       {URL: wide, CA: &dotvirtv1alpha1.ConfigMapKeyRef{Name: "metrics-ca", Key: "."}},
			"ca key ..":                      {URL: wide, CA: &dotvirtv1alpha1.ConfigMapKeyRef{Name: "metrics-ca", Key: ".."}},
			"ca key starting with ..":        {URL: wide, CA: &dotvirtv1alpha1.ConfigMapKeyRef{Name: "metrics-ca", Key: "..ca"}},
			"ca with a bad ConfigMap name":   {URL: wide, CA: &dotvirtv1alpha1.ConfigMapKeyRef{Name: "Metrics_CA", Key: "ca.crt"}},
		} {
			dv := testCR()
			dv.Spec.Metrics = m
			if err := admin.Create(ctx, dv, client.DryRunAll); !apierrors.IsInvalid(err) {
				t.Errorf("%s: got %v, want Invalid", name, err)
			}
		}
		// Unstructured: the typed client drops an empty url before it is sent.
		emptyURL := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt",
			"metadata": map[string]any{"name": "empty-metrics-url", "namespace": "dotvirt"},
			"spec":     map[string]any{"metrics": map[string]any{"url": "", "tenancyURL": tenancy}},
		}}
		if err := admin.Create(ctx, emptyURL, client.DryRunAll); !apierrors.IsInvalid(err) {
			t.Errorf("tenancyURL with an empty url: got %v, want Invalid", err)
		}
		for name, m := range map[string]dotvirtv1alpha1.MetricsSpec{
			"OpenShift's querier alone":       {URL: "https://thanos-querier.openshift-monitoring.svc.cluster.local:9091"},
			"both endpoints and a CA":         {URL: wide, TenancyURL: tenancy + "/", CA: ca},
			"plain http in-cluster":           {URL: "http://prometheus-operated.monitoring.svc:9090", TenancyURL: "http://prom-label-proxy.monitoring.svc:8080"},
			"ca for an https tenancyURL only": {URL: "http://prom.monitoring.svc:9090", TenancyURL: tenancy, CA: ca},
			"bracketed IPv6 tenancyURL":       {URL: "https://[fd00::10]:9091", TenancyURL: "https://[fd00::10]:9092"},
			"ca key starting with one dot":    {URL: wide, CA: &dotvirtv1alpha1.ConfigMapKeyRef{Name: "metrics-ca", Key: ".ca.crt"}},
			// url shipped without validation, so any stored value still admits.
			"url with an uppercase scheme": {URL: "HTTPS://thanos-querier.openshift-monitoring.svc:9091"},
			"url with a trailing-dot FQDN": {URL: "https://prom.monitoring.svc.cluster.local.:9091"},
		} {
			dv := testCR()
			dv.Spec.Metrics = m
			if err := admin.Create(ctx, dv, client.DryRunAll); client.IgnoreAlreadyExists(err) != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})

	t.Run("admission takes a delegated forge only with its OAuth2 application", func(t *testing.T) {
		const authorize = "https://forge.example/login/oauth/authorize"
		const forge = "http://forgejo.forge.svc:3000"
		ok := func() *dotvirtv1alpha1.ForgeUserAuthSpec {
			return &dotvirtv1alpha1.ForgeUserAuthSpec{
				ClientID: "dotvirt", ClientSecret: dotvirtv1alpha1.SecretKeyRef{Name: "dotvirt-forge-app", Key: "secret"},
				AuthorizeURL: authorize,
			}
		}
		for name, mod := range map[string]func(*dotvirtv1alpha1.ForgeSpec){
			"Delegated without userAuth": func(f *dotvirtv1alpha1.ForgeSpec) { f.PullRequests, f.URL = "Delegated", forge },
			// The forge users' tokens go to must come from the spec, never
			// from the credential Secret's url.
			"Delegated without url": func(f *dotvirtv1alpha1.ForgeSpec) { f.PullRequests, f.UserAuth = "Delegated", ok() },
			"an unknown mode":       func(f *dotvirtv1alpha1.ForgeSpec) { f.PullRequests = "Bots" },
			"authorizeURL with a query": func(f *dotvirtv1alpha1.ForgeSpec) {
				f.PullRequests, f.UserAuth, f.URL = "Delegated", ok(), forge
				f.UserAuth.AuthorizeURL = authorize + "?x=1"
			},
			"authorizeURL with credentials": func(f *dotvirtv1alpha1.ForgeSpec) {
				f.PullRequests, f.UserAuth, f.URL = "Delegated", ok(), forge
				f.UserAuth.AuthorizeURL = "https://u:p@forge.example/login/oauth/authorize"
			},
			"authorizeURL not http": func(f *dotvirtv1alpha1.ForgeSpec) {
				f.PullRequests, f.UserAuth, f.URL = "Delegated", ok(), forge
				f.UserAuth.AuthorizeURL = "forge.example/login/oauth/authorize"
			},
			"tokenURL with a fragment": func(f *dotvirtv1alpha1.ForgeSpec) {
				f.PullRequests, f.UserAuth, f.URL = "Delegated", ok(), forge
				f.UserAuth.TokenURL = "https://forgejo.lan/login/oauth/access_token#f"
			},
			"no clientID": func(f *dotvirtv1alpha1.ForgeSpec) {
				f.PullRequests, f.UserAuth, f.URL = "Delegated", ok(), forge
				f.UserAuth.ClientID = ""
			},
			"no client secret key": func(f *dotvirtv1alpha1.ForgeSpec) {
				f.PullRequests, f.UserAuth, f.URL = "Delegated", ok(), forge
				f.UserAuth.ClientSecret.Key = ""
			},
		} {
			dv := testCR()
			mod(&dv.Spec.Forge)
			if err := admin.Create(ctx, dv, client.DryRunAll); !apierrors.IsInvalid(err) {
				t.Errorf("%s: got %v, want Invalid", name, err)
			}
		}
		// No authorizeURL: the typed client sends the empty string, which the
		// pattern refuses like a missing field.
		noAuthorize := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "dotvirt.io/v1alpha1", "kind": "Dotvirt",
			"metadata": map[string]any{"name": "no-authorize", "namespace": "dotvirt"},
			"spec": map[string]any{"forge": map[string]any{"url": forge, "pullRequests": "Delegated", "userAuth": map[string]any{
				"clientID": "dotvirt", "clientSecret": map[string]any{"name": "dotvirt-forge-app", "key": "secret"},
			}}},
		}}
		if err := admin.Create(ctx, noAuthorize, client.DryRunAll); !apierrors.IsInvalid(err) {
			t.Errorf("userAuth without authorizeURL: got %v, want Invalid", err)
		}
		for name, mod := range map[string]func(*dotvirtv1alpha1.ForgeSpec){
			"Delegated with its application": func(f *dotvirtv1alpha1.ForgeSpec) {
				f.PullRequests, f.UserAuth, f.URL = "Delegated", ok(), forge
			},
			"and a LAN token endpoint": func(f *dotvirtv1alpha1.ForgeSpec) {
				f.PullRequests, f.UserAuth, f.URL = "Delegated", ok(), forge
				f.UserAuth.TokenURL = "http://forgejo.forge.svc:3000/login/oauth/access_token"
			},
			// The stored values keep admitting: the mode list only grew.
			"Users":   func(f *dotvirtv1alpha1.ForgeSpec) { f.PullRequests = "Users" },
			"Dotvirt": func(f *dotvirtv1alpha1.ForgeSpec) { f.PullRequests = "Dotvirt" },
			// userAuth set ahead of switching the mode is harmless.
			"userAuth with the bot mode": func(f *dotvirtv1alpha1.ForgeSpec) { f.UserAuth = ok() },
		} {
			dv := testCR()
			mod(&dv.Spec.Forge)
			if err := admin.Create(ctx, dv, client.DryRunAll); client.IgnoreAlreadyExists(err) != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})

	t.Run("admission takes one sign-in flow", func(t *testing.T) {
		oidc := &dotvirtv1alpha1.OIDCSpec{IssuerURL: "https://idm.example/oauth2/openid/dotvirt", ClientID: "dotvirt"}
		dv := testCR()
		dv.Spec.Auth = dotvirtv1alpha1.AuthSpec{OpenShiftSSO: new(true), OIDC: oidc}
		if err := admin.Create(ctx, dv, client.DryRunAll); !apierrors.IsInvalid(err) {
			t.Errorf("openShiftSSO with oidc: got %v, want Invalid", err)
		}
		// Unset resolves off beside oidc, so only an explicit true conflicts.
		dv = testCR()
		dv.Spec.Auth = dotvirtv1alpha1.AuthSpec{OpenShiftSSO: new(false), OIDC: oidc}
		if err := admin.Create(ctx, dv, client.DryRunAll); client.IgnoreAlreadyExists(err) != nil {
			t.Errorf("openShiftSSO false with oidc: %v", err)
		}
		for _, u := range []string{"http://idm.example", "idm.example", "https://idm.example/a b", "https://idm.example?x"} {
			dv := testCR()
			dv.Spec.Auth.OIDC = &dotvirtv1alpha1.OIDCSpec{IssuerURL: u, ClientID: "dotvirt"}
			if err := admin.Create(ctx, dv, client.DryRunAll); !apierrors.IsInvalid(err) {
				t.Errorf("issuer %q: got %v, want Invalid", u, err)
			}
		}
		dv = testCR()
		dv.Spec.Auth.OIDC = oidc
		if err := admin.Create(ctx, dv, client.DryRunAll); client.IgnoreAlreadyExists(err) != nil {
			t.Errorf("oidc alone: %v", err)
		}
	})

	t.Run("an oidc install that never named openShiftSSO upgrades as it is", func(t *testing.T) {
		// Stored as an earlier operator wrote it: oidc set, openShiftSSO absent.
		// Every later write must pass admission, and the operator never writes
		// the resolved value back. A second install halts before the finalizer,
		// so the operator reads it and the subtest can delete it freely.
		mustCreate(t, admin, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sso-upgrade"}})
		dv := testCR()
		dv.Namespace = "sso-upgrade"
		dv.Spec.Auth.OIDC = &dotvirtv1alpha1.OIDCSpec{IssuerURL: "https://idm.example/oauth2/openid/dotvirt", ClientID: "dotvirt"}
		mustCreate(t, admin, dv)
		t.Cleanup(func() { _ = admin.Delete(context.Background(), dv) })
		got := &dotvirtv1alpha1.Dotvirt{}
		eventually(t, "the operator read the install", func() bool {
			return admin.Get(ctx, client.ObjectKeyFromObject(dv), got) == nil && len(got.Status.Conditions) > 0
		})
		if got.Spec.Auth.OpenShiftSSO != nil {
			t.Errorf("stored openShiftSSO = %v, want absent", *got.Spec.Auth.OpenShiftSSO)
		}
		// A merge patch carries no resourceVersion: the operator's own status
		// writes cannot make it conflict.
		base := got.DeepCopy()
		got.Spec.Auth.OIDC.DisplayName = "Company login"
		if err := admin.Patch(ctx, got, client.MergeFrom(base)); err != nil {
			t.Errorf("updating the oidc install: %v", err)
		}
	})

	t.Run("admission refuses branding the app refuses", func(t *testing.T) {
		// The app's own checks (internal/branding) refuse each of these at
		// startup: admission refusing them first keeps dotvirt from a
		// crashloop.
		link := func(label, url string) []dotvirtv1alpha1.BrandingLink {
			return []dotvirtv1alpha1.BrandingLink{{Label: label, URL: url}}
		}
		six := make([]dotvirtv1alpha1.BrandingLink, 6)
		for i := range six {
			six[i] = dotvirtv1alpha1.BrandingLink{Label: "a", URL: "https://c.example"}
		}
		for name, b := range map[string]dotvirtv1alpha1.BrandingSpec{
			"long name":           {ProductName: strings.Repeat("a", 41)},
			"name newline":        {ProductName: "Uni\nwatt"},
			"name C1":             {ProductName: "Uni\u0085watt"},
			"six links":           {Links: six},
			"empty label":         {Links: link("", "https://c.example")},
			"long label":          {Links: link(strings.Repeat("a", 33), "https://c.example")},
			"label control":       {Links: link("a\x7fb", "https://c.example")},
			"http":                {Links: link("a", "http://c.example")},
			"javascript":          {Links: link("a", "javascript:alert(1)")},
			"credentials":         {Links: link("a", "https://u:p@c.example")},
			"no host":             {Links: link("a", "https:///account")},
			"space":               {Links: link("a", "https://c.example/a b")},
			"bad escape":          {Links: link("a", "https://c.example/%zz")},
			"control in url":      {Links: link("a", "https://c.example/a\x01b")},
			"configmap uppercase": {ConfigMap: "Theme"},
			"icon path":           {ConfigMap: "t", Links: []dotvirtv1alpha1.BrandingLink{{Label: "a", URL: "https://c.example", Icon: "../theme.css"}}},
			"icon url":            {ConfigMap: "t", Links: []dotvirtv1alpha1.BrandingLink{{Label: "a", URL: "https://c.example", Icon: "https://c.example/a.svg"}}},
			"icon type":           {ConfigMap: "t", Links: []dotvirtv1alpha1.BrandingLink{{Label: "a", URL: "https://c.example", Icon: "a.gif"}}},
			"icon long":           {ConfigMap: "t", Links: []dotvirtv1alpha1.BrandingLink{{Label: "a", URL: "https://c.example", Icon: strings.Repeat("a", 61) + ".svg"}}},
			"icon no configmap":   {Links: []dotvirtv1alpha1.BrandingLink{{Label: "a", URL: "https://c.example", Icon: "a.svg"}}},
		} {
			dv := testCR()
			dv.Spec.Branding = b
			if err := admin.Create(ctx, dv, client.DryRunAll); !apierrors.IsInvalid(err) {
				t.Errorf("%s: got %v, want Invalid", name, err)
			}
		}
		dv := testCR()
		dv.Spec.Branding = dotvirtv1alpha1.BrandingSpec{
			ProductName: "Uniwatt Cloud", ConfigMap: "uniwatt-theme",
			Links: append(link("Billing", "https://c.example/billing?tab=open#due"),
				dotvirtv1alpha1.BrandingLink{Label: "Projects", URL: "https://c.example/", Icon: "projects.svg"}),
		}
		if err := admin.Create(ctx, dv, client.DryRunAll); client.IgnoreAlreadyExists(err) != nil {
			t.Errorf("a provider's branding: %v", err)
		}
		// Text links need no ConfigMap: CRs from before link icons stay valid.
		text := testCR()
		text.Spec.Branding = dotvirtv1alpha1.BrandingSpec{Links: link("Billing", "https://c.example/billing")}
		if err := admin.Create(ctx, text, client.DryRunAll); client.IgnoreAlreadyExists(err) != nil {
			t.Errorf("text links without a ConfigMap: %v", err)
		}
	})

	t.Run("admission refuses allowed tenant kinds without sync impersonation", func(t *testing.T) {
		off, on := false, true
		deploy := []dotvirtv1alpha1.GroupKind{{Group: "apps", Kind: "Deployment"}}
		dv := testCR()
		dv.Spec.ArgoCD.TenantKindsAllowed = deploy
		dv.Spec.ArgoCD.SyncImpersonation.Enabled = &off
		if err := admin.Create(ctx, dv, client.DryRunAll); !apierrors.IsInvalid(err) {
			t.Errorf("allowed kinds with impersonation off: got %v, want Invalid", err)
		}
		for name, enabled := range map[string]*bool{"unset": nil, "on": &on} {
			dv := testCR()
			dv.Spec.ArgoCD.TenantKindsAllowed = append(deploy, dotvirtv1alpha1.GroupKind{Kind: "Service"})
			dv.Spec.ArgoCD.SyncImpersonation.Enabled = enabled
			if err := admin.Create(ctx, dv, client.DryRunAll); client.IgnoreAlreadyExists(err) != nil {
				t.Errorf("allowed kinds with impersonation %s: %v", name, err)
			}
		}
	})

	t.Run("admission refuses a whole group or a malformed kind", func(t *testing.T) {
		for _, bad := range []dotvirtv1alpha1.GroupKind{{Group: "apps", Kind: "*"}, {Group: "apps"}, {Group: "Apps", Kind: "Deployment"}, {Group: "*", Kind: "Deployment"}} {
			dv := testCR()
			dv.Spec.ArgoCD.TenantKindsAllowed = []dotvirtv1alpha1.GroupKind{bad}
			if err := admin.Create(ctx, dv, client.DryRunAll); !apierrors.IsInvalid(err) {
				t.Errorf("%+v: got %v, want Invalid", bad, err)
			}
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

	t.Run("a status-only change does not re-run the pass", func(t *testing.T) {
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
		settle(t, admin, dv, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: install.AppName, Namespace: "dotvirt"}})
		before := passes.Load()
		base := dv.DeepCopy()
		dv.Status.Phase = "Probe"
		if err := admin.Status().Patch(ctx, dv, client.MergeFrom(base)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		if n := passes.Load() - before; n != 0 {
			t.Errorf("a status-only change ran %d passes", n)
		}
	})

	t.Run("a moved ArgoCD namespace is swept by name", func(t *testing.T) {
		dv := &dotvirtv1alpha1.Dotvirt{}
		if err := admin.Get(ctx, types.NamespacedName{Namespace: "dotvirt", Name: "x"}, dv); err != nil {
			t.Fatal(err)
		}
		mustCreate(t, admin, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "argocd-moved"}})
		moveTo := func(ns string) {
			value := "null"
			if ns != "" {
				value = strconv.Quote(ns)
			}
			patch := fmt.Appendf(nil, `{"spec":{"argocd":{"namespace":%s}}}`, value)
			if err := admin.Patch(ctx, dv, client.RawPatch(types.MergePatchType, patch)); err != nil {
				t.Fatal(err)
			}
		}
		// What the install writes in the ArgoCD namespace.
		written := func(ns string) int {
			n := 0
			for _, obj := range []client.Object{
				&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: install.SyncRequestName}},
				&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: install.AppsetConfigMapName}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: install.AppsetSecretName}},
				argoObj("ApplicationSet", "", install.ApplicationSetName, nil),
				argoObj("AppProject", "", "dotvirt-p-team-a", nil),
			} {
				if admin.Get(ctx, types.NamespacedName{Namespace: ns, Name: obj.GetName()}, obj) == nil {
					n++
				}
			}
			return n
		}
		recorded := func() string {
			got := &dotvirtv1alpha1.Dotvirt{}
			if admin.Get(ctx, client.ObjectKeyFromObject(dv), got) != nil {
				return ""
			}
			return strings.Join(got.Status.ArgoNamespaces, ",")
		}
		eventually(t, "argocd recorded", func() bool { return recorded() == "argocd" && written("argocd") == 5 })
		moveTo("argocd-moved")
		eventually(t, "argocd swept after the move", func() bool {
			return recorded() == "argocd-moved" && written("argocd-moved") == 5 && written("argocd") == 0
		})
		moveTo("")
		eventually(t, "argocd-moved swept after the move back", func() bool {
			return recorded() == "argocd" && written("argocd") == 5 && written("argocd-moved") == 0
		})
	})

	// envtest runs no aggregation controller: admin takes the rules OpenShift
	// aggregates into it (testdata, read from a cluster), RoleBinding writes
	// included, NetworkAttachmentDefinition writes and EgressFirewalls not.
	adminRole := &rbacv1.ClusterRole{}
	if err := admin.Get(ctx, types.NamespacedName{Name: "admin"}, adminRole); err != nil {
		t.Fatal(err)
	}
	adminRole.Rules = readClusterRoles(t, filepath.Join("testdata", "openshift-admin.json"))[0].Rules
	adminRole.AggregationRule = nil
	if err := admin.Update(ctx, adminRole); err != nil {
		t.Fatal(err)
	}
	// syncGrants are the platform repo's grants of ns to project's owner and
	// sync account, as the app renders them (testdata, kept by the netgen
	// package's TestGrantFixture).
	grantFixture, err := os.ReadFile(filepath.Join("testdata", "project-grants.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	syncGrants := func(ns, project string) {
		rendered := strings.NewReplacer("x-ns", ns, "x-project", project).Replace(string(grantFixture))
		dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(rendered), 4096)
		for {
			rb := &rbacv1.RoleBinding{}
			if err := dec.Decode(rb); errors.Is(err, io.EOF) {
				return
			} else if err != nil {
				t.Fatal(err)
			}
			mustCreate(t, admin, rb)
		}
	}
	kube := kubernetes.NewForConfigOrDie(adminCfg)
	review := func(user string, groups []string, attrs authzv1.ResourceAttributes) bool {
		res, err := kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{
			User: user, Groups: groups, ResourceAttributes: &attrs,
		}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return res.Status.Allowed
	}

	t.Run("an admin's credential grant reaches that one Secret only", func(t *testing.T) {
		// The operator's ServiceAccount holds nothing else here (the suite binds
		// its role to a user), so each review sees the grant alone.
		account := "system:serviceaccount:dotvirt-operator:" + install.OperatorServiceAccount
		groups := []string{"system:serviceaccounts", "system:serviceaccounts:dotvirt-operator"}
		for _, c := range []struct {
			ns      string
			managed bool
		}{{"cred-byo", false}, {"cred-managed", true}} {
			mustCreate(t, admin, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: c.ns}})
			// Not the install the deletion subtest takes down: its labels differ.
			// The owner never exists, and envtest runs no garbage collector.
			dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "cred", Namespace: c.ns, UID: "0f7c1d2e-cred"}}
			dv.Spec.Forge.CredentialsSecret, dv.Spec.Forge.Managed = "byo-creds", c.managed
			role, binding := install.ForgeCredentialGrant(dv, "dotvirt-operator")
			// Either half landing (and, below, going) is a pass at once, not one
			// on the drift timer: the binding first here, its Role after.
			before := passes.Load()
			mustCreate(t, admin, binding)
			soon(t, "a pass after the grant's binding lands", func() bool { return passes.Load() > before })
			before = passes.Load()
			mustCreate(t, admin, role)
			soon(t, "a pass after the grant's Role lands", func() bool { return passes.Load() > before })
			for _, r := range []struct {
				verb, ns, name string
				want           bool
			}{
				{"get", c.ns, "byo-creds", true},
				{"create", c.ns, "byo-creds", c.managed},
				{"patch", c.ns, "byo-creds", c.managed},
				{"update", c.ns, "byo-creds", false},
				{"delete", c.ns, "byo-creds", false},
				{"get", c.ns, install.SessionSecretName, false},
				{"create", c.ns, "other", false},
				{"list", c.ns, "", false},
				{"watch", c.ns, "", false},
				{"get", "dotvirt", "byo-creds", false},
				{"get", "kube-system", "byo-creds", false},
			} {
				if got := review(account, groups, authzv1.ResourceAttributes{Verb: r.verb, Resource: "secrets", Namespace: r.ns, Name: r.name}); got != r.want {
					t.Errorf("%s: %s secrets %s/%s: allowed=%v, want %v", c.ns, r.verb, r.ns, r.name, got, r.want)
				}
			}
		}
		before := passes.Load()
		if err := admin.Delete(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: install.ForgeCredentialGrantName, Namespace: "cred-byo"}}); err != nil {
			t.Fatal(err)
		}
		soon(t, "a pass after the grant is revoked", func() bool { return passes.Load() > before })

		// A half grant (the Role alone) is what GrantIncomplete reports: its
		// replace commands, run as a cluster admin would, end with the grant.
		mustCreate(t, admin, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cred-half"}})
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "cred", Namespace: "cred-half", UID: "0f7c1d2e-cred"}}
		dv.Spec.Forge.CredentialsSecret = "byo-creds"
		role, _ := install.ForgeCredentialGrant(dv, "dotvirt-operator")
		mustCreate(t, admin, role)
		clusterAdmin, err := env.AddUser(envtest.User{Name: "grant-admin", Groups: []string{"system:masters"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		kubectl, err := clusterAdmin.Kubectl()
		if err != nil {
			t.Fatal(err)
		}
		// The README's commands, each variable as an admin derives it.
		vars := strings.NewReplacer(`"$INSTALL_NS"`, "cred-half", `"$VERBS"`, "get", `"$CREDENTIAL"`, "byo-creds",
			`"$OPERATOR"`, "dotvirt-operator:"+install.OperatorServiceAccount)
		for _, cmd := range strings.Split(vars.Replace(adminobjects.CredentialGrantCommands), "\n") {
			stdout, stderr, err := kubectl.Run(strings.Fields(cmd)[1:]...)
			if err != nil {
				out, _ := io.ReadAll(stdout)
				msg, _ := io.ReadAll(stderr)
				t.Fatalf("%s: %v: %s %s", cmd, err, out, msg)
			}
		}
		if !review(account, groups, authzv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: "cred-half", Name: "byo-creds"}) {
			t.Error("the replace commands left the operator without the grant")
		}

		// As the console installs it: its own render (adminobjects.Render for the
		// pins it reads), server-side applied under the cluster admin's token
		// with the console's field manager, forced. The operator's watch sees it
		// land, and the operator's account reads exactly that Secret.
		mustCreate(t, admin, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cred-console"}})
		p := adminobjects.Params{
			Operator: account, Namespace: "cred-console", ForgeSecret: "byo-creds", ForgeManaged: true,
			InstallName: "cred", InstallUID: "0f7c1d2e-cred",
		}
		dyn := dynamic.NewForConfigOrDie(adminCfg)
		allowed := adminobjects.Allowed(p.Namespace)
		before = passes.Load()
		var installed int
		for _, o := range adminobjects.Render(p) {
			ref, ok := adminobjects.Allow(allowed, o.Object)
			if !ok || ref.Namespace == "" {
				continue
			}
			if _, err := dyn.Resource(ref.GVR()).Namespace(ref.Namespace).Apply(ctx, ref.Name, o.Object, metav1.ApplyOptions{FieldManager: "dotvirt", Force: true}); err != nil {
				t.Fatalf("install %s %s: %v", ref.Kind, ref.Name, err)
			}
			installed++
		}
		if installed != 2 {
			t.Fatalf("the console's render held %d namespaced objects, want the grant's Role and RoleBinding", installed)
		}
		soon(t, "a pass after the console installs the grant", func() bool { return passes.Load() > before })
		for _, r := range []struct {
			verb, ns, name string
			want           bool
		}{
			{"get", "cred-console", "byo-creds", true},
			{"create", "cred-console", "byo-creds", true},
			{"patch", "cred-console", "byo-creds", true},
			{"delete", "cred-console", "byo-creds", false},
			{"list", "cred-console", "", false},
			{"get", "cred-console", install.SessionSecretName, false},
			{"get", "kube-system", "byo-creds", false},
		} {
			if got := review(account, groups, authzv1.ResourceAttributes{Verb: r.verb, Resource: "secrets", Namespace: r.ns, Name: r.name}); got != r.want {
				t.Errorf("console grant: %s secrets %s/%s: allowed=%v, want %v", r.verb, r.ns, r.name, got, r.want)
			}
		}
	})

	t.Run("a project's sync account holds its project's rights only", func(t *testing.T) {
		mustCreate(t, admin, projectNS("team-c", "team-c", "https://forge.example/dotvirt/team-c.git"))
		syncGrants("team-a", "team-a")
		syncGrants("team-c", "team-c")
		user := "system:serviceaccount:argocd:dotvirt-p-team-a"
		groups := []string{"system:serviceaccounts", "system:serviceaccounts:argocd", "system:authenticated"}
		for _, c := range []struct {
			verb, group, resource, sub, name, ns string
			want                                 bool
		}{
			{"create", "kubevirt.io", "virtualmachines", "", "", "team-a", true},
			{"create", "cdi.kubevirt.io", "datavolumes", "", "", "team-a", true},
			{"create", "k8s.ovn.org", "userdefinednetworks", "", "", "team-a", true},
			{"create", "networking.k8s.io", "networkpolicies", "", "", "team-a", true},
			{"create", "k8s.ovn.org", "egressfirewalls", "", "", "team-a", true},
			// A clone out of its own namespace only.
			{"create", "cdi.kubevirt.io", "datavolumes", "source", "", "team-a", true},
			{"create", "cdi.kubevirt.io", "datavolumes", "source", "", "team-c", false},
			// admin may only read a NetworkAttachmentDefinition: a raw one can
			// join another project's VLAN.
			{"create", "k8s.cni.cncf.io", "network-attachment-definitions", "", "", "team-a", false},
			// admin may write RoleBindings in its namespace, but never one past
			// its own rights; that a repo declares none is the AppProject's
			// whitelist, below.
			{"create", "rbac.authorization.k8s.io", "rolebindings", "", "", "team-a", true},
			{"bind", "rbac.authorization.k8s.io", "clusterroles", "", "cluster-admin", "team-a", false},
			{"create", "kubevirt.io", "virtualmachines", "", "", "team-c", false},
			{"create", "k8s.ovn.org", "egressfirewalls", "", "", "team-c", false},
			{"create", "kubevirt.io", "virtualmachines", "", "", "", false},
		} {
			attrs := authzv1.ResourceAttributes{Verb: c.verb, Group: c.group, Resource: c.resource, Subresource: c.sub, Name: c.name, Namespace: c.ns}
			if got := review(user, groups, attrs); got != c.want {
				t.Errorf("team-a's sync account %s %s/%s/%s %s in %q: allowed=%v, want %v", c.verb, c.group, c.resource, c.sub, c.name, c.ns, got, c.want)
			}
			// The project's people hold what its repo holds.
			if got := review("team-a-owner", []string{"system:authenticated"}, attrs); got != c.want {
				t.Errorf("team-a's owner %s %s/%s/%s %s in %q: allowed=%v, want %v", c.verb, c.group, c.resource, c.sub, c.name, c.ns, got, c.want)
			}
		}
		ap := argoObj("AppProject", "", "", nil)
		if err := admin.Get(ctx, types.NamespacedName{Namespace: "argocd", Name: "dotvirt-p-team-a"}, ap); err != nil {
			t.Fatal(err)
		}
		kinds, _, _ := unstructured.NestedSlice(ap.Object, "spec", "namespaceResourceWhitelist")
		for _, k := range kinds {
			if kind := k.(map[string]any)["kind"]; kind == "RoleBinding" || kind == "NetworkAttachmentDefinition" {
				t.Errorf("the tenant AppProject admits %s", kind)
			}
		}
		// The account is a name: nothing is created for it.
		if err := admin.Get(ctx, types.NamespacedName{Namespace: "argocd", Name: "dotvirt-p-team-a"}, &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
			t.Errorf("sync ServiceAccount object: %v, want none", err)
		}
	})

	t.Run("sync impersonation flips once nothing would stop syncing", func(t *testing.T) {
		dv := &dotvirtv1alpha1.Dotvirt{}
		if err := admin.Get(ctx, types.NamespacedName{Namespace: "dotvirt", Name: "x"}, dv); err != nil {
			t.Fatal(err)
		}
		inst := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"server": map[string]any{"insecure": true}}}}
		inst.SetGroupVersionKind(install.ArgoCDGVK)
		inst.SetNamespace("argocd")
		inst.SetName("argocd")
		mustCreate(t, admin, inst)
		// Another team's Application, in an AppProject mapping no account.
		billing := argoObj("AppProject", "argocd", "billing", nil)
		billing.Object["spec"] = map[string]any{"sourceRepos": []any{"*"}}
		mustCreate(t, admin, billing)
		app := argoObj("Application", "argocd", "billing", nil)
		app.Object["spec"] = map[string]any{"project": "billing", "destination": map[string]any{"name": "in-cluster", "namespace": "billing"}}
		mustCreate(t, admin, app)
		// A merge patch carries no resourceVersion: the operator writing the CR
		// between a read and an update cannot make the flip conflict.
		setEnabled := func(on bool) {
			patch := fmt.Appendf(nil, `{"spec":{"argocd":{"syncImpersonation":{"enabled":%t}}}}`, on)
			if err := admin.Patch(ctx, dv, client.RawPatch(types.MergePatchType, patch)); err != nil {
				t.Fatal(err)
			}
		}
		setEnabled(true)
		reason := func() string {
			got := &dotvirtv1alpha1.Dotvirt{}
			if admin.Get(ctx, client.ObjectKeyFromObject(dv), got) != nil {
				return ""
			}
			if c := apimeta.FindStatusCondition(got.Status.Conditions, dotvirtv1alpha1.ConditionSyncImpersonationReady); c != nil && c.ObservedGeneration == got.Generation {
				return c.Reason + ": " + c.Message
			}
			return ""
		}
		key := func() any {
			if admin.Get(ctx, client.ObjectKeyFromObject(inst), inst) != nil {
				return nil
			}
			extra, _, _ := unstructured.NestedMap(inst.Object, "spec", "extraConfig")
			return extra[install.SyncImpersonationKey]
		}
		// team-z (namespace team-b) holds no grant, the billing Application
		// no account, the controller no impersonation right.
		for _, want := range []string{"project team-z's sync account holds no grant in team-b", "argocd/billing", "cannot impersonate ServiceAccounts in argocd"} {
			eventually(t, "held back saying "+want, func() bool { return strings.Contains(reason(), want) })
		}
		if key() != nil {
			t.Fatal("set while held back")
		}
		// Each fixed with no CR edit: the verdict naming them is rechecked.
		syncGrants("team-b", "team-z")
		billing.Object["spec"].(map[string]any)["destinationServiceAccounts"] = []any{
			map[string]any{"server": "*", "namespace": "*", "defaultServiceAccount": "argocd:argocd-application-controller"},
		}
		if err := admin.Update(ctx, billing); err != nil {
			t.Fatal(err)
		}
		mustCreate(t, admin, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "argocd-impersonate"},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"serviceaccounts"}, Verbs: []string{"impersonate"}}},
		})
		mustCreate(t, admin, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "argocd-impersonate", Namespace: "argocd"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "argocd-impersonate"},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "argocd-application-controller", Namespace: "argocd"}},
		})
		eventually(t, "impersonation set", func() bool { return key() == "true" })
		eventually(t, "Pending until argocd-cm carries it", func() bool { return strings.HasPrefix(reason(), "Pending") })
		if _, ok, _ := unstructured.NestedMap(inst.Object, "spec", "server"); !ok {
			t.Errorf("the flip dropped a field it does not own: %v", inst.Object["spec"])
		}
		// Standing in for the argocd-operator, which copies extraConfig there.
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "argocd-cm", Namespace: "argocd"},
			Data: map[string]string{install.SyncImpersonationKey: "true"}}
		mustCreate(t, admin, cm)
		eventually(t, "Enabled", func() bool { return strings.HasPrefix(reason(), "Enabled") })

		setEnabled(false)
		eventually(t, "impersonation removed", func() bool { return key() == nil })
		if err := admin.Delete(ctx, cm); err != nil {
			t.Fatal(err)
		}
		// On again for the uninstall below, which must take it off.
		setEnabled(true)
		eventually(t, "impersonation set again", func() bool { return key() == "true" })
	})

	// The operator's apply holds one key of a Secret the gitops operator runs:
	// a key another manager set stays theirs, and only what the operator wrote
	// is ever removed.
	t.Run("argocd-secret's webhook keys are taken only when free", func(t *testing.T) {
		op, err := client.New(opCfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		r := newReconciler(op, depsOK)
		// Each namespace stands for both the install's, holding its webhook
		// secret "dotvirt", and Argo's, holding argocd-secret written to data
		// as the argocd-operator and kubectl do.
		argoSecret := func(ns string, data map[string]string) *dotvirtv1alpha1.Dotvirt {
			t.Helper()
			mustCreate(t, admin, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
			mustCreate(t, admin, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: install.ArgoWebhookSecretName, Namespace: ns},
				Data:       map[string][]byte{"secret": []byte("dotvirt")},
			})
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: install.ArgoSecretName, Namespace: ns}, Data: map[string][]byte{}}
			for k, v := range data {
				s.Data[k] = []byte(v)
			}
			if err := admin.Create(ctx, s, client.FieldOwner("argocd-operator")); err != nil {
				t.Fatal(err)
			}
			return &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: ns}}
		}
		data := func(ns string) map[string][]byte {
			t.Helper()
			var s corev1.Secret
			if err := admin.Get(ctx, types.NamespacedName{Namespace: ns, Name: install.ArgoSecretName}, &s); err != nil {
				t.Fatal(err)
			}
			return s.Data
		}

		argoSecret("argo-held", map[string]string{install.ArgoWebhookKey: "declared"})
		if err := r.applyArgoWebhookKey(ctx, "argo-held", "dotvirt", "https://argo/api/webhook"); !apierrors.IsConflict(err) {
			t.Errorf("apply over another manager's key: err = %v, want a conflict", err)
		}
		if got := string(data("argo-held")[install.ArgoWebhookKey]); got != "declared" {
			t.Errorf("another manager's key = %q, want it kept", got)
		}

		// What an operator before 0.2.3 left: its secret under the key Argo never
		// reads, written through stringData.
		upgraded := argoSecret("argo-upgrade", map[string]string{"admin.password": "x"})
		legacy := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: install.ArgoSecretName, Namespace: "argo-upgrade"},
			StringData: map[string]string{install.LegacyArgoWebhookKey: "dotvirt"},
		}
		if err := install.Apply(ctx, op, legacy, false); err != nil {
			t.Fatal(err)
		}
		if err := r.applyArgoWebhookKey(ctx, "argo-upgrade", "dotvirt", "https://argo/api/webhook"); err != nil {
			t.Fatal(err)
		}
		got := data("argo-upgrade")
		if _, left := got[install.LegacyArgoWebhookKey]; left || string(got[install.ArgoWebhookKey]) != "dotvirt" || string(got["admin.password"]) != "x" {
			t.Errorf("upgraded argocd-secret keys = %v, want %s set, %s gone, admin.password kept", slices.Sorted(maps.Keys(got)), install.ArgoWebhookKey, install.LegacyArgoWebhookKey)
		}
		if err := r.releaseArgoWebhookKey(ctx, upgraded, "argo-upgrade"); err != nil {
			t.Fatal(err)
		}
		if got := data("argo-upgrade"); len(got) != 1 || string(got["admin.password"]) != "x" {
			t.Errorf("released argocd-secret keys = %v, want admin.password only", slices.Sorted(maps.Keys(got)))
		}

		foreign := argoSecret("argo-foreign", map[string]string{install.LegacyArgoWebhookKey: "theirs"})
		if err := r.applyArgoWebhookKey(ctx, "argo-foreign", "dotvirt", "https://argo/api/webhook"); err != nil {
			t.Fatal(err)
		}
		if err := r.releaseArgoWebhookKey(ctx, foreign, "argo-foreign"); err != nil {
			t.Fatal(err)
		}
		if got := data("argo-foreign"); len(got) != 1 || string(got[install.LegacyArgoWebhookKey]) != "theirs" {
			t.Errorf("argocd-secret keys = %v, want only the other manager's %s", slices.Sorted(maps.Keys(got)), install.LegacyArgoWebhookKey)
		}

		if err := r.releaseArgoWebhookKey(ctx, foreign, "argo-absent"); err != nil {
			t.Errorf("release with no argocd-secret: %v", err)
		}
	})

	t.Run("deletion cleans up as the operator", func(t *testing.T) {
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
		// A name only an older version rendered: prune reads only the names this
		// one renders, so it stays.
		legacy := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "dotvirt-legacy", Labels: install.Labels(dv)},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "dotvirt"},
		}
		mustCreate(t, admin, legacy)
		// Our instance label, another install's namespace: prune lists AppProjects
		// in the ArgoCD namespace, and OwnedBy keeps the unscoped delete off it.
		foreign := argoObj("AppProject", "argocd", "dotvirt-p-foreign",
			map[string]string{install.InstanceLabel: dv.Name, install.InstanceNamespaceLabel: "elsewhere"})
		mustCreate(t, admin, foreign)
		// The operator's key in Argo's own Secret, beside a key it never set.
		argoSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: install.ArgoSecretName, Namespace: "argocd"},
			Data:       map[string][]byte{"admin.password": []byte("x")},
		}
		if err := admin.Create(ctx, argoSecret, client.FieldOwner("argocd-operator")); err != nil {
			t.Fatal(err)
		}
		op, err := client.New(opCfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		var hmac corev1.Secret
		if err := admin.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: install.ArgoWebhookSecretName}, &hmac); err != nil {
			t.Fatal(err)
		}
		value := string(hmac.Data["secret"])
		// First the key an operator before 0.2.3 wrote, unread by Argo, then the
		// one Argo verifies against: an upgrade not yet reconciled.
		oldKey := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: install.ArgoSecretName, Namespace: "argocd"},
			StringData: map[string]string{install.LegacyArgoWebhookKey: value},
		}
		for _, s := range []*corev1.Secret{oldKey, install.ArgoWebhookSecret("argocd", value)} {
			if err := install.ApplyShared(ctx, op, s, false); err != nil {
				t.Fatal(err)
			}
		}
		mark := ops.len()
		if err := admin.Delete(ctx, dv); err != nil {
			t.Fatal(err)
		}
		eventually(t, "CR gone", func() bool {
			return apierrors.IsNotFound(admin.Get(ctx, client.ObjectKeyFromObject(dv), &dotvirtv1alpha1.Dotvirt{}))
		})
		// Before prune took dotvirt's AppProjects, the setting came off the
		// shared instance, where the billing Application still syncs.
		inst := &unstructured.Unstructured{}
		inst.SetGroupVersionKind(install.ArgoCDGVK)
		if err := admin.Get(ctx, types.NamespacedName{Namespace: "argocd", Name: "argocd"}, inst); err != nil {
			t.Fatal(err)
		}
		if extra, _, _ := unstructured.NestedMap(inst.Object, "spec", "extraConfig"); extra[install.SyncImpersonationKey] != nil {
			t.Errorf("uninstalled, extraConfig = %v", extra)
		}
		if err := admin.Get(ctx, client.ObjectKeyFromObject(argoSecret), argoSecret); err != nil {
			t.Fatal(err)
		}
		if got := argoSecret.Data; len(got) != 1 || string(got["admin.password"]) != "x" {
			t.Errorf("uninstalled, argocd-secret keys = %v, want admin.password only", slices.Sorted(maps.Keys(got)))
		}
		done := ops.since(mark)
		released := slices.Index(done, "release argocd")
		deleted := slices.IndexFunc(done, func(op string) bool { return strings.HasPrefix(op, "delete AppProject") })
		if released < 0 || deleted < 0 || released > deleted {
			t.Errorf("uninstall did %v, want the key released before any AppProject goes", done)
		}
		left := map[string]bool{}
		for _, list := range []client.ObjectList{
			&rbacv1.ClusterRoleBindingList{},
			&rbacv1.RoleBindingList{},
			&corev1.SecretList{},
			&corev1.ConfigMapList{},
			&appsv1.DeploymentList{},
			&corev1.ServiceList{},
			&corev1.ServiceAccountList{},
			unstructuredList(install.ArgoGVK("AppProject"))(),
			unstructuredList(install.ArgoGVK("ApplicationSet"))(),
		} {
			if err := admin.List(ctx, list, client.MatchingLabels{install.InstanceLabel: dv.Name}); err != nil {
				t.Fatal(err)
			}
			items, err := apimeta.ExtractList(list)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				obj := item.(client.Object)
				// The install namespace's state is ownerRef-GC'd, and envtest runs no GC.
				if _, isSecret := obj.(*corev1.Secret); isSecret && obj.GetNamespace() == dv.Namespace {
					continue
				}
				left[obj.GetNamespace()+"/"+obj.GetName()] = true
			}
		}
		if len(left) != 2 || !left["/"+legacy.Name] || !left["argocd/"+foreign.GetName()] {
			t.Errorf("left after deletion: %v, want only the legacy binding and the other install's AppProject", left)
		}
	})

	// The install is gone, so a namespace admin's Dotvirt is now the oldest in
	// the cluster. The suite's install namespace is dotvirt, not the operator's
	// own, which is the admin override at work; team-a is outside it.
	// The install-namespace subtests run under the guard a cluster admin
	// installs for the suite's install, in force before they start.
	guarded := func(t *testing.T) {
		t.Helper()
		p := adminobjects.Params{Operator: operatorUser, Namespace: "dotvirt", ArgoNamespace: "argocd", ArgoController: "argocd-application-controller",
			ForgeSecret: adminobjects.DefaultForgeSecret}
		objs := adminobjects.Render(p)
		for _, o := range objs {
			if err := admin.Apply(ctx, client.ApplyConfigurationFromUnstructured(o.Object.DeepCopy()), client.FieldOwner("dotvirt"), client.ForceOwnership); err != nil {
				t.Fatalf("install %s: %v", o.Object.GetName(), err)
			}
		}
		opClient, err := client.New(opCfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		// A Secret the operator's role allows by name anywhere, which the
		// guard refuses outside the install and ArgoCD namespaces.
		probe := func() error {
			m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&corev1.Secret{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
				ObjectMeta: metav1.ObjectMeta{Name: install.OAuthSecretName, Namespace: "team-a"},
			})
			return opClient.Apply(context.Background(), client.ApplyConfigurationFromUnstructured(&unstructured.Unstructured{Object: m}),
				client.FieldOwner(operatorFieldManager), client.ForceOwnership, client.DryRunAll)
		}
		eventually(t, "the guard in force", func() bool {
			err := probe()
			return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "dotvirt operator guard")
		})
		t.Cleanup(func() {
			for _, o := range objs {
				_ = admin.Delete(context.Background(), o.Object)
			}
			eventually(t, "the guard lifted", func() bool { return probe() == nil })
		})
	}

	t.Run("a Dotvirt outside the install namespace gets nothing even when oldest", func(t *testing.T) {
		guarded(t)
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{
			Name: "x", Namespace: "team-a", Finalizers: []string{dotvirtFinalizer},
		}}
		mustCreate(t, admin, dv)
		eventually(t, "WrongNamespace status", func() bool {
			var got dotvirtv1alpha1.Dotvirt
			if admin.Get(ctx, client.ObjectKeyFromObject(dv), &got) != nil {
				return false
			}
			c := apimeta.FindStatusCondition(got.Status.Conditions, dotvirtv1alpha1.ConditionAvailable)
			return c != nil && c.Reason == "WrongNamespace"
		})
		var crbs rbacv1.ClusterRoleBindingList
		if err := admin.List(ctx, &crbs); err != nil {
			t.Fatal(err)
		}
		for _, b := range crbs.Items {
			for _, s := range b.Subjects {
				if s.Namespace == "team-a" {
					t.Errorf("ClusterRoleBinding %s binds %s in team-a", b.Name, s.Name)
				}
			}
		}
		if err := admin.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: install.AppName}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
			t.Errorf("console Deployment in team-a: err=%v, want NotFound", err)
		}
		// The finalizer its creator set is dropped on deletion, with no prune.
		if err := admin.Delete(ctx, dv); err != nil {
			t.Fatal(err)
		}
		eventually(t, "ignored Dotvirt gone", func() bool {
			return apierrors.IsNotFound(admin.Get(ctx, client.ObjectKeyFromObject(dv), &dotvirtv1alpha1.Dotvirt{}))
		})
	})

	// An upgrade with no override and no Dotvirt in the operator's namespace
	// adopts the install ClusterRoleBinding dotvirt names, read as the operator.
	t.Run("the operator adopts the install its binding names", func(t *testing.T) {
		guarded(t)
		// Outside the suite's install namespace, so its manager leaves it be.
		mustCreate(t, admin, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "adopted"}})
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "adopted"}}
		mustCreate(t, admin, dv)
		defer func() { _ = admin.Delete(ctx, dv) }()
		// The binding comes after the Dotvirt it was made for.
		time.Sleep(1100 * time.Millisecond)
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: install.AppName},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: install.AppName},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: install.AppName, Namespace: "adopted"}},
		}
		mustCreate(t, admin, crb)
		defer func() { _ = admin.Delete(ctx, crb) }()
		op, err := client.New(opCfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		r := &DotvirtReconciler{Client: op, APIReader: op, OperatorNamespace: "dotvirt-operator"}
		ns, adopted, err := r.installNamespace(ctx)
		if err != nil || ns != "adopted" || !adopted {
			t.Errorf("installNamespace = (%q, %v, %v), want (adopted, true, nil)", ns, adopted, err)
		}
	})
}

// runManager starts the controller as cmd/main.go wires it, under cfg, and
// counts its install passes.
// opLog records, in order, the writes the reconciler makes that ordering
// claims are about: the sync impersonation key set or released on the ArgoCD
// resource, and AppProject deletes.
type opLog struct {
	mu  sync.Mutex
	ops []string
}

func (l *opLog) add(op string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops = append(l.ops, op)
}

func (l *opLog) since(mark int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.ops[mark:])
}

func (l *opLog) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ops)
}

// loggingClient is the reconciler's client, recording into an opLog.
type loggingClient struct {
	client.Client
	log *opLog
}

func (c loggingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if obj.GetObjectKind().GroupVersionKind() == install.ArgoCDGVK {
		if u, ok := obj.(*unstructured.Unstructured); ok && u.Object["spec"] == nil {
			c.log.add("release " + obj.GetName())
		} else {
			c.log.add("set " + obj.GetName())
		}
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c loggingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if obj.GetObjectKind().GroupVersionKind() == install.ArgoGVK("AppProject") {
		c.log.add("delete AppProject " + obj.GetName())
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func runManager(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme) (*atomic.Int64, *opLog) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Cache:                  cache.Options{ByObject: CacheByObject(platform.Kubernetes)},
		Client:                 client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.ConfigMap{}, &corev1.Secret{}, &rbacv1.RoleBinding{}, &rbacv1.Role{}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	passes := &atomic.Int64{}
	ops := &opLog{}
	r := &DotvirtReconciler{
		Client:           loggingClient{mgr.GetClient(), ops},
		Scheme:           mgr.GetScheme(),
		Config:           mgr.GetConfig(),
		Platform:         platform.Kubernetes,
		APIReader:        mgr.GetAPIReader(),
		OperatorUsername: operatorUser,
		// The suite's installs all live in dotvirt; the tenant subtest proves
		// a Dotvirt elsewhere gets nothing.
		InstallNamespace: "dotvirt",
		syncCache:        syncCache{syncRecheck: time.Second, syncTTL: time.Second},
		probe: func(c *rest.Config) (deps.Result, error) {
			passes.Add(1)
			return depsOK(c)
		},
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
	return passes, ops
}

// argoStubCRDs registers the argoproj.io kinds the install applies, schemaless:
// the suite checks apply and RBAC, not Argo's own validation.
func argoStubCRDs() []*apiextensionsv1.CustomResourceDefinition {
	preserve := true
	var out []*apiextensionsv1.CustomResourceDefinition
	for _, gvk := range []struct{ kind, plural, version string }{
		{"AppProject", "appprojects", "v1alpha1"},
		{"Application", "applications", "v1alpha1"},
		{"ApplicationSet", "applicationsets", "v1alpha1"},
		{"ArgoCD", "argocds", "v1beta1"},
	} {
		kind, plural := gvk.kind, gvk.plural
		out = append(out, &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: plural + ".argoproj.io"},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "argoproj.io",
				Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: kind, ListKind: kind + "List", Plural: plural, Singular: strings.ToLower(kind)},
				Scope: apiextensionsv1.NamespaceScoped,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name: gvk.version, Served: true, Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object", XPreserveUnknownFields: &preserve,
					}},
				}},
			},
		})
	}
	return out
}

// exposureStubCRDs serve the Route and HTTPRoute kinds the console may be
// exposed by, so the front-door rules have something to judge.
func exposureStubCRDs() []*apiextensionsv1.CustomResourceDefinition {
	preserve, intOrString := true, true
	str := apiextensionsv1.JSONSchemaProps{Type: "string"}
	num := apiextensionsv1.JSONSchemaProps{Type: "integer"}
	open := func(props map[string]apiextensionsv1.JSONSchemaProps) apiextensionsv1.JSONSchemaProps {
		return apiextensionsv1.JSONSchemaProps{Type: "object", XPreserveUnknownFields: &preserve, Properties: props}
	}
	list := func(item apiextensionsv1.JSONSchemaProps) apiextensionsv1.JSONSchemaProps {
		return apiextensionsv1.JSONSchemaProps{Type: "array", Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: &item}}
	}
	backend := open(map[string]apiextensionsv1.JSONSchemaProps{"kind": str, "name": str, "weight": num})
	// The fields the guard's rules read, typed as these kinds' own schemas
	// type them, so the rules type-check as on a cluster serving them.
	specs := map[string]apiextensionsv1.JSONSchemaProps{
		"routes": open(map[string]apiextensionsv1.JSONSchemaProps{
			"host": str, "path": str, "wildcardPolicy": str, "to": backend, "alternateBackends": list(backend),
			"port": open(map[string]apiextensionsv1.JSONSchemaProps{"targetPort": {XIntOrString: intOrString}}),
			"tls":  open(map[string]apiextensionsv1.JSONSchemaProps{"termination": str, "destinationCACertificate": str, "insecureEdgeTerminationPolicy": str}),
		}),
		"httproutes": open(map[string]apiextensionsv1.JSONSchemaProps{
			"hostnames": list(str), "parentRefs": list(open(nil)),
			"rules": list(open(map[string]apiextensionsv1.JSONSchemaProps{
				"filters": list(open(nil)), "matches": list(open(nil)),
				"backendRefs": list(open(map[string]apiextensionsv1.JSONSchemaProps{
					"group": str, "kind": str, "namespace": str, "name": str, "port": num, "weight": num, "filters": list(open(nil)),
				})),
			})),
		}),
	}
	var out []*apiextensionsv1.CustomResourceDefinition
	for _, k := range []struct{ group, kind, plural string }{
		{"route.openshift.io", "Route", "routes"},
		{"gateway.networking.k8s.io", "HTTPRoute", "httproutes"},
	} {
		meta := metav1.ObjectMeta{Name: k.plural + "." + k.group}
		if strings.HasSuffix(k.group, ".k8s.io") {
			// The apiserver asks this of any CRD in a Kubernetes-owned group.
			meta.Annotations = map[string]string{"api-approved.kubernetes.io": "unapproved, test stub"}
		}
		root := open(map[string]apiextensionsv1.JSONSchemaProps{"spec": specs[k.plural], "status": open(nil)})
		out = append(out, &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: meta,
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: k.group,
				Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: k.kind, ListKind: k.kind + "List", Plural: k.plural, Singular: strings.ToLower(k.kind)},
				Scope: apiextensionsv1.NamespaceScoped,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name: "v1", Served: true, Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &root},
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

// settle waits until the objects' resourceVersions hold still, so the passes
// their last writes triggered have run.
func settle(t *testing.T, c client.Client, objs ...client.Object) {
	t.Helper()
	versions := func() string {
		var vs []string
		for _, obj := range objs {
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj); err != nil {
				t.Fatal(err)
			}
			vs = append(vs, obj.GetResourceVersion())
		}
		return strings.Join(vs, ",")
	}
	last, stableSince := versions(), time.Now()
	eventually(t, "objects settled", func() bool {
		if v := versions(); v != last {
			last, stableSince = v, time.Now()
		}
		return time.Since(stableSince) > 1500*time.Millisecond
	})
}

// soon is eventually for what a watch delivers: seconds, well inside any timer.
func soon(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within 5s: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
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
