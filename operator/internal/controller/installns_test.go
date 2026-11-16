package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

// tenantCR is a Dotvirt a namespace admin created in their own namespace: the
// admin and edit roles let them.
func tenantCR() *dotvirtv1alpha1.Dotvirt {
	dv := testCR()
	dv.Namespace = "team-a"
	dv.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	return dv
}

// The oldest Dotvirt in the cluster, outside the install namespace, still
// provisions nothing: no finalizer, no binding to an account in its namespace,
// no workload, and only a status saying where the install lives.
func TestDotvirtOutsideTheInstallNamespaceProvisionsNothing(t *testing.T) {
	dv := tenantCR()
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)

	if res := reconcileOnce(t, r, dv); res != (ctrl.Result{}) {
		t.Errorf("result = %+v: an ignored Dotvirt is never requeued", res)
	}
	got := getCR(t, c, dv)
	if co := cond(got, dotvirtv1alpha1.ConditionAvailable); co == nil || co.Status != metav1.ConditionFalse || co.Reason != "WrongNamespace" {
		t.Errorf("Available = %+v, want False/WrongNamespace", co)
	}
	if controllerutil.ContainsFinalizer(got, dotvirtFinalizer) {
		t.Error("an ignored Dotvirt got the finalizer")
	}
	var crbs rbacv1.ClusterRoleBindingList
	if err := c.List(context.Background(), &crbs); err != nil {
		t.Fatal(err)
	}
	if len(crbs.Items) != 0 {
		t.Errorf("an ignored Dotvirt provisioned %d ClusterRoleBindings", len(crbs.Items))
	}
	if exists(t, c, &appsv1.Deployment{}, dv.Namespace, install.AppName) {
		t.Error("an ignored Dotvirt got the console Deployment")
	}
}

// Its status is written once: later events about the same Dotvirt write nothing.
func TestIgnoredDotvirtStatusIsWrittenOnce(t *testing.T) {
	dv := tenantCR()
	writes := 0
	c := testBuilder(t).WithObjects(dv).WithInterceptorFuncs(interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
			writes++
			return c.SubResource(sub).Patch(ctx, o, p, opts...)
		},
	}).Build()
	r := newReconciler(c, depsOK)
	for range 3 {
		reconcileOnce(t, r, dv)
	}
	if writes != 1 {
		t.Errorf("status writes = %d over 3 passes, want 1", writes)
	}
}

// An admin naming another namespace makes the Dotvirt there the install.
func TestInstallNamespaceOverrideAdmitsThatNamespace(t *testing.T) {
	dv := testCR()
	dv.Namespace = "dotvirt-staging"
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)
	r.InstallNamespace = "dotvirt-staging"

	reconcileOnce(t, r, dv)
	got := getCR(t, c, dv)
	if !controllerutil.ContainsFinalizer(got, dotvirtFinalizer) {
		t.Error("the Dotvirt in the named install namespace was not taken as the install")
	}
	if co := cond(got, dotvirtv1alpha1.ConditionAvailable); co != nil && co.Reason == "WrongNamespace" {
		t.Errorf("Available = %+v in the named namespace", co)
	}
}

// With no install namespace known, no Dotvirt is acted on.
func TestNoInstallNamespaceActsOnNone(t *testing.T) {
	dv := testCR()
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)
	r.InstallNamespace = ""

	reconcileOnce(t, r, dv)
	got := getCR(t, c, dv)
	if controllerutil.ContainsFinalizer(got, dotvirtFinalizer) {
		t.Error("a Dotvirt was taken as the install with no install namespace")
	}
	if co := cond(got, dotvirtv1alpha1.ConditionAvailable); co == nil || co.Reason != "WrongNamespace" {
		t.Errorf("Available = %+v, want False/WrongNamespace", co)
	}
}

// An older Dotvirt outside the install namespace never makes the install wait
// as a second install.
func TestOlderDotvirtElsewhereDoesNotBlockTheInstall(t *testing.T) {
	tenant := tenantCR()
	dv := testCR()
	dv.CreationTimestamp = metav1.Now()
	c := testBuilder(t).WithObjects(tenant, dv).Build()
	r := newReconciler(c, depsOK)

	reconcileOnce(t, r, dv)
	got := getCR(t, c, dv)
	if co := cond(got, dotvirtv1alpha1.ConditionAvailable); co != nil && co.Reason == "Conflict" {
		t.Errorf("Available = %+v: a Dotvirt outside the install namespace counted as an older install", co)
	}
	if !controllerutil.ContainsFinalizer(got, dotvirtFinalizer) {
		t.Error("the install did not proceed")
	}
}

// Deleting a Dotvirt outside the install namespace that carries the operator's
// finalizer (set by its creator, or left from before an upgrade) drops the
// finalizer and prunes nothing: prune matches objects by instance labels, and
// the real install here shares its name.
func TestIgnoredDotvirtDeletionPrunesNothing(t *testing.T) {
	now := metav1.Now()
	tenant := tenantCR()
	tenant.Finalizers = []string{dotvirtFinalizer}
	tenant.DeletionTimestamp = &now
	inst := testCR()
	crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "dotvirt", Labels: map[string]string{install.InstanceLabel: inst.Name}}}
	c := testBuilder(t).WithObjects(tenant, inst, crb).Build()
	r := newReconciler(c, depsOK)

	reconcileOnce(t, r, tenant)
	if exists(t, c, &dotvirtv1alpha1.Dotvirt{}, tenant.Namespace, tenant.Name) {
		t.Error("the ignored Dotvirt is still there: its finalizer was not dropped")
	}
	if !exists(t, c, &rbacv1.ClusterRoleBinding{}, "", "dotvirt") {
		t.Error("deleting an ignored Dotvirt pruned the install's ClusterRoleBinding")
	}
}

// Events about an install's inputs wake only the Dotvirt in the install namespace.
func TestInstallsEnqueuesOnlyTheInstallNamespace(t *testing.T) {
	c := testBuilder(t).WithObjects(testCR(), tenantCR()).Build()
	r := newReconciler(c, depsOK)
	reqs := r.installs(context.Background(), nil)
	if len(reqs) != 1 || reqs[0].Namespace != "dotvirt" {
		t.Errorf("installs = %v, want only dotvirt/x", reqs)
	}
}
