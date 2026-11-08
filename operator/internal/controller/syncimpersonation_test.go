package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

const argoSAUser = "system:serviceaccount:argocd:"

// reviews answers SubjectAccessReviews with allow and counts them; every
// other Create reaches the fake.
func reviews(allow func(account string, a *authzv1.ResourceAttributes) bool, n *atomic.Int64) interceptor.Funcs {
	return interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		sar, ok := obj.(*authzv1.SubjectAccessReview)
		if !ok {
			return cl.Create(ctx, obj, opts...)
		}
		n.Add(1)
		if !slices.Contains(sar.Spec.Groups, "system:serviceaccounts:argocd") {
			return nil
		}
		sar.Status.Allowed = allow(strings.TrimPrefix(sar.Spec.User, argoSAUser), sar.Spec.ResourceAttributes)
		return nil
	}}
}

// granted is the cluster the platform repo's grants make: each project's sync
// account may write in its own namespaces only, the platform account holds its
// operand roles, and nothing else is granted.
func granted(projects map[string][]string) func(string, *authzv1.ResourceAttributes) bool {
	return func(account string, a *authzv1.ResourceAttributes) bool {
		if account == tenancy.PlatformSyncAccount {
			return true
		}
		return a.Namespace != "" && slices.Contains(projects[account], a.Namespace)
	}
}

func argoCDInstance(extra map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"server": map[string]any{"route": map[string]any{"enabled": true}}}}}
	u.SetGroupVersionKind(install.ArgoCDGVK)
	u.SetNamespace("argocd")
	u.SetName("argocd")
	if extra != nil {
		u.Object["spec"].(map[string]any)["extraConfig"] = extra
	}
	return u
}

func extraConfig(t *testing.T, c client.Client) map[string]any {
	t.Helper()
	inst := argoCDInstance(nil)
	if !exists(t, c, inst, "argocd", "argocd") {
		t.Fatal("ArgoCD instance gone")
	}
	if _, ok, _ := unstructured.NestedMap(inst.Object, "spec", "server"); !ok {
		t.Errorf("a field the operator does not own was dropped: %v", inst.Object["spec"])
	}
	extra, _, _ := unstructured.NestedMap(inst.Object, "spec", "extraConfig")
	return extra
}

func syncCond(t *testing.T, c client.Client, dv *dotvirtv1alpha1.Dotvirt) *metav1.Condition {
	t.Helper()
	got := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionSyncImpersonationReady)
	if got == nil {
		t.Fatal("no SyncImpersonationReady condition")
	}
	return got
}

// Every tier names its sync account: each tenant AppProject its project's,
// the platform AppProject the platform's. The accounts exist beside them,
// owned by their AppProjects for the garbage collector, and the platform
// account holds the apply role and its own.
func TestSyncAccountsRendered(t *testing.T) {
	forgeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer forgeDown.Close()
	dv, creds := byoForgeCR(forgeDown.URL)
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, creds, projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(granted(nil), &n)).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)

	for appProject, account := range map[string]string{"dotvirt-p-team-a": "dotvirt-p-team-a", "dotvirt-platform": tenancy.PlatformSyncAccount} {
		ap := argoObj("AppProject", "", "", nil)
		if !exists(t, c, ap, "argocd", appProject) {
			t.Fatalf("AppProject %s missing", appProject)
		}
		entries, _, _ := unstructured.NestedSlice(ap.Object, "spec", "destinationServiceAccounts")
		if len(entries) != 1 || entries[0].(map[string]any)["defaultServiceAccount"] != "argocd:"+account || entries[0].(map[string]any)["namespace"] != "*" {
			t.Errorf("%s destinationServiceAccounts = %v, want argocd:%s", appProject, entries, account)
		}
		var sa corev1.ServiceAccount
		if !exists(t, c, &sa, "argocd", account) {
			t.Fatalf("sync account %s missing", account)
		}
		if len(sa.OwnerReferences) != 1 || sa.OwnerReferences[0].Kind != "AppProject" || sa.OwnerReferences[0].Name != appProject {
			t.Errorf("%s owners = %+v, want its AppProject", account, sa.OwnerReferences)
		}
		if !install.OwnedBy(sa.Labels, dv) {
			t.Errorf("%s labels = %v, want the install's", account, sa.Labels)
		}
	}
	var apply rbacv1.ClusterRoleBinding
	exists(t, c, &apply, "", "dotvirt-argocd-apply")
	var names []string
	for _, s := range apply.Subjects {
		names = append(names, s.Namespace+"/"+s.Name)
	}
	if !slices.Equal(names, []string{"argocd/argocd-application-controller", "argocd/" + tenancy.PlatformSyncAccount}) {
		t.Errorf("dotvirt-argocd-apply subjects = %v, want the controller and the platform account", names)
	}
	var platformSync rbacv1.ClusterRoleBinding
	if !exists(t, c, &platformSync, "", tenancy.PlatformSyncAccount) || platformSync.RoleRef.Name != tenancy.PlatformSyncAccount {
		t.Errorf("platform sync binding = %+v", platformSync)
	}
	var dep appsv1.Deployment
	exists(t, c, &dep, dv.Namespace, install.AppName)
	if v, ok := envValue(dep.Spec.Template.Spec.Containers[0].Env, "DOTVIRT_ARGO_NAMESPACE"); !ok || v != "argocd" {
		t.Errorf("DOTVIRT_ARGO_NAMESPACE = %q, %v", v, ok)
	}
	// Not asked: nothing is touched on the instance, and the condition says so.
	if got := syncCond(t, c, dv); got.Status != metav1.ConditionUnknown || got.Reason != "Disabled" {
		t.Errorf("condition = %+v", got)
	}
}

func envValue(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// The operand roles carry the names pkg/tenancy gives the app's grants.
func TestSyncOperandRoles(t *testing.T) {
	roles := map[string]*rbacv1.ClusterRole{}
	for _, r := range readClusterRoles(t, filepath.Join("..", "..", "config", "rbac", "operand_roles.yaml")) {
		roles[r.Name] = r
	}
	egress := roles[tenancy.TenantEgressRole]
	if egress == nil || len(egress.Rules) != 1 || !slices.Equal(egress.Rules[0].Resources, []string{"egressfirewalls"}) {
		t.Errorf("%s = %+v, want EgressFirewall writes alone", tenancy.TenantEgressRole, egress)
	}
	platformSync := roles[tenancy.PlatformSyncAccount]
	if platformSync == nil {
		t.Fatalf("no %s role", tenancy.PlatformSyncAccount)
	}
	var bind []string
	for _, rule := range platformSync.Rules {
		if slices.Contains(rule.Verbs, "escalate") || slices.Contains(rule.Resources, "*") {
			t.Errorf("%s grants %+v", tenancy.PlatformSyncAccount, rule)
		}
		if slices.Contains(rule.Verbs, "bind") {
			bind = append(bind, rule.ResourceNames...)
		}
	}
	if !slices.Equal(bind, []string{"admin", tenancy.TenantEgressRole}) {
		t.Errorf("%s binds %v, want exactly the roles project grants name", tenancy.PlatformSyncAccount, bind)
	}
}

// Asked, and every project's account holds its grants and no more: the
// operator sets the one key on the instance and leaves the rest of its spec
// alone. Asked no longer: the key goes.
func TestSyncImpersonationFlipAndRollback(t *testing.T) {
	dv := testCR()
	dv.Spec.ArgoCD.SyncImpersonation.Enabled = true
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, argoCDInstance(nil),
		projectNS("a-prod", "team-a", "dotvirt/team-a.git"), projectNS("a-dev", "team-a", "dotvirt/team-a.git"),
		projectNS("b", "team-b", "dotvirt/team-b.git"),
	).WithInterceptorFuncs(reviews(granted(map[string][]string{"dotvirt-p-team-a": {"a-dev", "a-prod"}, "dotvirt-p-team-b": {"b"}}), &n)).
		WithReturnManagedFields().Build()
	r := newReconciler(c, depsOK)
	now := time.Now()
	r.now = func() time.Time { return now }
	reconcileOnce(t, r, dv)

	if got := extraConfig(t, c)[install.SyncImpersonationKey]; got != "true" {
		t.Fatalf("%s = %v, want true", install.SyncImpersonationKey, got)
	}
	if got := syncCond(t, c, dv); got.Status != metav1.ConditionTrue || got.Reason != "Enabled" {
		t.Errorf("condition = %+v", got)
	}
	// 3 per project namespace, 1 per project.
	if got := n.Load(); got != 3*3+2 {
		t.Errorf("%d reviews, want 11", got)
	}
	// Once on, the grants are asked again, and that verdict serves the passes
	// that follow until its inputs change.
	reconcileOnce(t, r, dv)
	reconcileOnce(t, r, dv)
	if got := n.Load(); got != 22 {
		t.Errorf("%d reviews after three passes, want 22", got)
	}
	// RBAC moves are not watched: an aged verdict is asked again, which is how
	// a merged grant is seen.
	now = now.Add(driftResync / 2)
	reconcileOnce(t, r, dv)
	if got := n.Load(); got != 33 {
		t.Errorf("%d reviews once the verdict aged out, want 33", got)
	}

	stored := getCR(t, c, dv)
	stored.Spec.ArgoCD.SyncImpersonation.Enabled = false
	if err := c.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, stored)
	if extra := extraConfig(t, c); extra[install.SyncImpersonationKey] != nil {
		t.Errorf("opted out, extraConfig = %v", extra)
	}
	if got := syncCond(t, c, dv); got.Status != metav1.ConditionUnknown || got.Reason != "Disabled" {
		t.Errorf("opted out, condition = %+v", got)
	}
}

// Held back: a missing grant, a grant reaching past a project's namespaces,
// another team's Application on the instance, or Applications still in the
// shared tier. Each is said; nothing is set.
func TestSyncImpersonationRefusals(t *testing.T) {
	foreignApp := argoObj("Application", "argocd", "billing", nil)
	foreignApp.Object["spec"] = map[string]any{"project": "default"}
	// Another instance's Application, which the setting does not reach.
	otherInstance := argoObj("Application", "team-argo", "web", nil)
	otherInstance.Object["spec"] = map[string]any{"project": "default"}
	sharedApp := application("dotvirt-team-a", install.TenantsProjectName)
	sharedTier := argoObj("AppProject", "argocd", install.TenantsProjectName, install.Labels(testCR()))
	own := map[string][]string{"dotvirt-p-team-a": {"team-a"}}
	for _, tc := range []struct {
		name   string
		allow  func(string, *authzv1.ResourceAttributes) bool
		objs   []client.Object
		ignore bool
		want   string
	}{
		{"missing grant", granted(nil), nil, false, "project team-a cannot sync VirtualMachines into team-a"},
		{"cluster-wide grant", func(account string, a *authzv1.ResourceAttributes) bool {
			return granted(own)(account, a) || account == "dotvirt-p-team-a"
		}, nil, false, "may create VirtualMachines in every namespace"},
		{"namespace open to every account", func(account string, a *authzv1.ResourceAttributes) bool {
			return granted(own)(account, a) || account == syncProbeAccount
		}, nil, false, "namespace team-a lets every ServiceAccount of argocd create VirtualMachines"},
		{"foreign Application", granted(own), []client.Object{foreignApp}, false, "argocd/billing"},
		{"shared tier in use", granted(own), []client.Object{sharedApp, sharedTier}, true, "shared dotvirt-tenants AppProject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dv := testCR()
			dv.Spec.ArgoCD.SyncImpersonation = dotvirtv1alpha1.SyncImpersonationSpec{Enabled: true, IgnoreForeignApplications: tc.ignore}
			var n atomic.Int64
			c := testBuilder(t).WithObjects(append(tc.objs, dv, argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git"))...).
				WithInterceptorFuncs(reviews(tc.allow, &n)).Build()
			reconcileOnce(t, newReconciler(c, depsOK), dv)
			if extra := extraConfig(t, c); extra[install.SyncImpersonationKey] != nil {
				t.Errorf("set while held back: %v", extra)
			}
			got := syncCond(t, c, dv)
			if got.Status != metav1.ConditionFalse || got.Reason != "NotReady" || !strings.Contains(got.Message, tc.want) {
				t.Errorf("condition = %+v, want NotReady saying %q", got, tc.want)
			}
		})
	}

	// Told to proceed past another team's Application, it does.
	dv := testCR()
	dv.Spec.ArgoCD.SyncImpersonation = dotvirtv1alpha1.SyncImpersonationSpec{Enabled: true, IgnoreForeignApplications: true}
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, foreignApp, argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(granted(own), &n)).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if extraConfig(t, c)[install.SyncImpersonationKey] != "true" {
		t.Error("ignoreForeignApplications did not proceed")
	}

	// Another instance's Applications do not hold this one back.
	dv = testCR()
	dv.Spec.ArgoCD.SyncImpersonation.Enabled = true
	c = testBuilder(t).WithObjects(dv, otherInstance, argoCDInstance(nil), projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(granted(own), &n)).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if extraConfig(t, c)[install.SyncImpersonationKey] != "true" {
		t.Errorf("held back by another instance's Application: %+v", syncCond(t, c, dv))
	}
}

// Once on, a project that loses its grants is reported, never a reason to
// turn impersonation off: it fails to sync rather than falling back to the
// controller's rights. A key set by hand is not the operator's to remove.
func TestSyncImpersonationStaysOn(t *testing.T) {
	dv := testCR()
	dv.Spec.ArgoCD.SyncImpersonation.Enabled = true
	byHand := argoCDInstance(map[string]any{install.SyncImpersonationKey: "true"})
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "argocd-cm", Namespace: "argocd"}, Data: map[string]string{install.SyncImpersonationKey: "true"}}
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, byHand, cm, projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(granted(nil), &n)).WithReturnManagedFields().Build()
	r := newReconciler(c, depsOK)
	reconcileOnce(t, r, dv)
	got := syncCond(t, c, dv)
	if got.Status != metav1.ConditionFalse || got.Reason != "Degraded" || !strings.Contains(got.Message, "project team-a") {
		t.Errorf("condition = %+v, want Degraded naming team-a", got)
	}

	stored := getCR(t, c, dv)
	stored.Spec.ArgoCD.SyncImpersonation.Enabled = false
	if err := c.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, stored)
	if extraConfig(t, c)[install.SyncImpersonationKey] != "true" {
		t.Error("removed a key the operator did not set")
	}
	if got := syncCond(t, c, dv); got.Status != metav1.ConditionTrue || !strings.Contains(got.Message, "outside dotvirt") {
		t.Errorf("condition = %+v, want Enabled outside dotvirt", got)
	}
}

// Off, with a controller that may create datavolumes/source cluster-wide: a
// tenant repo's DataVolume could clone a disk out of any namespace, so the
// condition says so.
func TestSyncImpersonationWarnsCloneAnywhere(t *testing.T) {
	dv := testCR()
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(func(account string, a *authzv1.ResourceAttributes) bool {
			return account == "argocd-application-controller" && a.Resource == "datavolumes" && a.Subresource == "source" && a.Namespace == ""
		}, &n)).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if got := syncCond(t, c, dv); got.Status != metav1.ConditionFalse || got.Reason != "CloneAnywhere" {
		t.Errorf("condition = %+v, want CloneAnywhere", got)
	}
	if n.Load() != 1 {
		t.Errorf("%d reviews while off, want the one clone check", n.Load())
	}
}

// Ready, but no ArgoCD resource to set it on: the condition hands the admin
// the setting instead.
func TestSyncImpersonationManualStep(t *testing.T) {
	dv := testCR()
	dv.Spec.ArgoCD.SyncImpersonation.Enabled = true
	var n atomic.Int64
	c := testBuilder(t).WithObjects(dv, projectNS("team-a", "team-a", "dotvirt/team-a.git")).
		WithInterceptorFuncs(reviews(granted(map[string][]string{"dotvirt-p-team-a": {"team-a"}}), &n)).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if got := syncCond(t, c, dv); got.Reason != "ManualStep" || !strings.Contains(got.Message, "argocd/argocd-cm") {
		t.Errorf("condition = %+v", got)
	}
}
