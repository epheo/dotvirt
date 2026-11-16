package controller

import (
	"context"
	"strings"
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

// adoptingReconciler runs as the operator does with no override: its own
// namespace is dotvirt-operator.
func adoptingReconciler(c client.Client) *DotvirtReconciler {
	r := newReconciler(c, depsOK)
	r.InstallNamespace = ""
	r.OperatorNamespace = "dotvirt-operator"
	return r
}

// dotvirtBinding is ClusterRoleBinding dotvirt as an earlier operator left it
// for the install in ns.
func dotvirtBinding(ns string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: install.AppName},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: install.AppName},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: install.AppName, Namespace: ns}},
	}
}

// An install an earlier operator provisioned outside the operator's namespace
// keeps working after the upgrade: adopted from its binding, reconciled, and
// told how to name it explicitly.
func TestUpgradeAdoptsTheInstallTheBindingNames(t *testing.T) {
	dv := testCR() // in dotvirt, as the earlier sample put it
	dv.Finalizers = []string{dotvirtFinalizer}
	c := testBuilder(t).WithObjects(dv, dotvirtBinding(dv.Namespace)).Build()
	r := adoptingReconciler(c)

	reconcileOnce(t, r, dv)
	got := getCR(t, c, dv)
	if co := cond(got, dotvirtv1alpha1.ConditionAvailable); co != nil && co.Reason == "WrongNamespace" {
		t.Fatalf("Available = %+v: the provisioned install was not adopted", co)
	}
	if co := cond(got, dotvirtv1alpha1.ConditionInstallNamespaceAdopted); co == nil || co.Status != metav1.ConditionTrue ||
		!strings.Contains(co.Message, InstallNamespaceEnv+"=dotvirt") {
		t.Errorf("InstallNamespaceAdopted = %+v, want True naming %s=dotvirt", co, InstallNamespaceEnv)
	}
	if !exists(t, c, &appsv1.Deployment{}, dv.Namespace, install.AppName) {
		t.Error("the adopted install was not reconciled: no console Deployment")
	}
	var crb rbacv1.ClusterRoleBinding
	if err := c.Get(context.Background(), client.ObjectKey{Name: install.AppName}, &crb); err != nil {
		t.Fatalf("ClusterRoleBinding dotvirt: %v", err)
	}
	if len(crb.Subjects) != 1 || crb.Subjects[0].Namespace != dv.Namespace {
		t.Errorf("ClusterRoleBinding dotvirt subjects = %v, want the install's account only", crb.Subjects)
	}
}

// After the adoption a namespace admin's Dotvirt, even an older one, is still
// ignored: the binding names the install's namespace, not theirs.
func TestTenantDotvirtStaysIgnoredWhenTheInstallIsAdopted(t *testing.T) {
	dv := testCR()
	tenant := tenantCR() // older than the install
	c := testBuilder(t).WithObjects(dv, tenant, dotvirtBinding(dv.Namespace)).Build()
	r := adoptingReconciler(c)

	reconcileOnce(t, r, tenant)
	got := getCR(t, c, tenant)
	if co := cond(got, dotvirtv1alpha1.ConditionAvailable); co == nil || co.Reason != "WrongNamespace" {
		t.Errorf("tenant Available = %+v, want False/WrongNamespace", co)
	}
	if controllerutil.ContainsFinalizer(got, dotvirtFinalizer) {
		t.Error("the tenant's Dotvirt got the finalizer")
	}
	reconcileOnce(t, r, dv)
	if co := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionAvailable); co != nil && (co.Reason == "WrongNamespace" || co.Reason == "Conflict") {
		t.Errorf("install Available = %+v: the older tenant Dotvirt counted", co)
	}
}

// No override, no Dotvirt in the operator's namespace and no binding: no
// install exists, and none is taken from a Dotvirt alone.
func TestNoBindingAndNoOwnDotvirtActsOnNone(t *testing.T) {
	dv := testCR()
	c := testBuilder(t).WithObjects(dv).Build()
	r := adoptingReconciler(c)

	reconcileOnce(t, r, dv)
	got := getCR(t, c, dv)
	if co := cond(got, dotvirtv1alpha1.ConditionAvailable); co == nil || co.Reason != "WrongNamespace" ||
		!strings.Contains(co.Message, "dotvirt-operator") {
		t.Errorf("Available = %+v, want False/WrongNamespace pointing at dotvirt-operator", co)
	}
	if controllerutil.ContainsFinalizer(got, dotvirtFinalizer) {
		t.Error("a Dotvirt was taken as the install with no install namespace")
	}
}

// A Dotvirt in the operator's own namespace is the install, over an adoption.
func TestOwnNamespaceWinsOverAdoption(t *testing.T) {
	own := testCR()
	own.Namespace = "dotvirt-operator"
	old := testCR()
	c := testBuilder(t).WithObjects(own, old, dotvirtBinding(old.Namespace)).Build()
	r := adoptingReconciler(c)

	reconcileOnce(t, r, old)
	if co := cond(getCR(t, c, old), dotvirtv1alpha1.ConditionAvailable); co == nil || co.Reason != "WrongNamespace" {
		t.Errorf("Available in dotvirt = %+v, want False/WrongNamespace", co)
	}
}

// The admin's override wins over an adoption.
func TestOverrideWinsOverAdoption(t *testing.T) {
	adoptable := testCR()
	named := testCR()
	named.Namespace = "dotvirt-staging"
	c := testBuilder(t).WithObjects(adoptable, named, dotvirtBinding(adoptable.Namespace)).Build()
	r := adoptingReconciler(c)
	r.InstallNamespace = "dotvirt-staging"

	reconcileOnce(t, r, adoptable)
	if co := cond(getCR(t, c, adoptable), dotvirtv1alpha1.ConditionAvailable); co == nil || co.Reason != "WrongNamespace" {
		t.Errorf("Available in dotvirt = %+v, want False/WrongNamespace", co)
	}
	reconcileOnce(t, r, named)
	got := getCR(t, c, named)
	if !controllerutil.ContainsFinalizer(got, dotvirtFinalizer) {
		t.Error("the Dotvirt in the named namespace was not taken as the install")
	}
	if co := cond(got, dotvirtv1alpha1.ConditionInstallNamespaceAdopted); co != nil {
		t.Errorf("InstallNamespaceAdopted = %+v under an explicit override", co)
	}
}

// Only the ServiceAccount dotvirt bound to ClusterRole dotvirt names an
// install: another subject or role is never adopted.
func TestAdoptionTrustsOnlyTheInstallSubject(t *testing.T) {
	for name, crb := range map[string]*rbacv1.ClusterRoleBinding{
		"another account": {
			ObjectMeta: metav1.ObjectMeta{Name: install.AppName},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: install.AppName},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "default", Namespace: "dotvirt"}},
		},
		"another role": {
			ObjectMeta: metav1.ObjectMeta{Name: install.AppName},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view"},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: install.AppName, Namespace: "dotvirt"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			dv := testCR()
			c := testBuilder(t).WithObjects(dv, crb).Build()
			reconcileOnce(t, adoptingReconciler(c), dv)
			if co := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionAvailable); co == nil || co.Reason != "WrongNamespace" {
				t.Errorf("Available = %+v, want False/WrongNamespace", co)
			}
		})
	}
}
