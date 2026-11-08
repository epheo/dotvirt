package controller

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
	"github.com/epheo/dotvirt/operator/internal/deps"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
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
			// The platform RoleBindings are read one by one, never listed or watched.
			{authzv1.ResourceAttributes{Verb: "get", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: "team-a", Name: "team-a-admins"}, true},
			{authzv1.ResourceAttributes{Verb: "list", Group: "rbac.authorization.k8s.io", Resource: "rolebindings"}, false},
			{authzv1.ResourceAttributes{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: "team-a"}, false},
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
	passes, ops := runManager(t, opCfg, scheme)

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
		appset := install.ApplicationSet(got, "argocd", nil, nil)
		if err := admin.Get(ctx, client.ObjectKeyFromObject(appset), argoObj("ApplicationSet", "", "", nil)); err != nil {
			t.Errorf("ApplicationSet: %v", err)
		}
		if err := admin.Get(ctx, types.NamespacedName{Namespace: "argocd", Name: "dotvirt-p-team-a"}, argoObj("AppProject", "", "", nil)); err != nil {
			t.Errorf("tenant AppProject: %v", err)
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
		setEnabled := func(on bool) {
			if err := admin.Get(ctx, client.ObjectKeyFromObject(dv), dv); err != nil {
				t.Fatal(err)
			}
			dv.Spec.ArgoCD.SyncImpersonation.Enabled = on
			if err := admin.Update(ctx, dv); err != nil {
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

	t.Run("deletion cleans up as the operator", func(t *testing.T) {
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
		// A name only an older version rendered: the name-scoped delete grant
		// refuses it, which must not wedge deletion.
		legacy := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "dotvirt-legacy", Labels: install.Labels(dv)},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "dotvirt"},
		}
		mustCreate(t, admin, legacy)
		// Our instance label, another install's namespace: prune lists it, and
		// OwnedBy is what keeps the unscoped AppProject delete off it.
		foreign := argoObj("AppProject", "argocd", "dotvirt-p-foreign",
			map[string]string{install.InstanceLabel: dv.Name, install.InstanceNamespaceLabel: "elsewhere"})
		mustCreate(t, admin, foreign)
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
		done := ops.since(mark)
		released := slices.Index(done, "release argocd")
		deleted := slices.IndexFunc(done, func(op string) bool { return strings.HasPrefix(op, "delete AppProject") })
		if released < 0 || deleted < 0 || released > deleted {
			t.Errorf("uninstall did %v, want the key released before any AppProject goes", done)
		}
		left := map[string]bool{}
		for _, list := range []client.ObjectList{
			&rbacv1.ClusterRoleBindingList{},
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
			t.Errorf("left after deletion: %v, want only the refused legacy binding and the other install's AppProject", left)
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
		Client:                 client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.ConfigMap{}, &corev1.Secret{}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	passes := &atomic.Int64{}
	ops := &opLog{}
	r := &DotvirtReconciler{
		Client:    loggingClient{mgr.GetClient(), ops},
		Scheme:    mgr.GetScheme(),
		Config:    mgr.GetConfig(),
		Platform:  platform.Kubernetes,
		APIReader: mgr.GetAPIReader(),
		syncCache: syncCache{syncRecheck: time.Second, syncTTL: time.Second},
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
