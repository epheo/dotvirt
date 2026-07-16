package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/deps"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

// testScheme mirrors the manager's scheme, plus the argoproj.io kinds registered
// as unstructured: a real API server resolves them via discovery, but the fake
// client's DeleteAllOf needs the kind AND its List kind in the scheme.
func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := dotvirtv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"AppProject", "Application", "ApplicationSet"} {
		gvk := install.ArgoGVK(kind)
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(kind+"List"), &unstructured.UnstructuredList{})
	}
	return s
}

func testCR() *dotvirtv1alpha1.Dotvirt {
	return &dotvirtv1alpha1.Dotvirt{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt", Generation: 1},
	}
}

func testBuilder(t *testing.T) *fake.ClientBuilder {
	return fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithStatusSubresource(&dotvirtv1alpha1.Dotvirt{})
}

// depsOK stubs the probe with a fully satisfied cluster.
func depsOK(*rest.Config) (deps.Result, error) { return deps.Result{}, nil }

func newReconciler(c client.Client, probe func(*rest.Config) (deps.Result, error)) *DotvirtReconciler {
	return &DotvirtReconciler{Client: c, Scheme: c.Scheme(), Platform: platform.Kubernetes, probe: probe}
}

func reconcileOnce(t *testing.T, r *DotvirtReconciler, dv *dotvirtv1alpha1.Dotvirt) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Namespace: dv.Namespace, Name: dv.Name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func getCR(t *testing.T, c client.Client, dv *dotvirtv1alpha1.Dotvirt) *dotvirtv1alpha1.Dotvirt {
	t.Helper()
	got := &dotvirtv1alpha1.Dotvirt{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: dv.Namespace, Name: dv.Name}, got); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	return got
}

func cond(dv *dotvirtv1alpha1.Dotvirt, condType string) *metav1.Condition {
	return apimeta.FindStatusCondition(dv.Status.Conditions, condType)
}

func exists(t *testing.T, c client.Client, obj client.Object, ns, name string) bool {
	t.Helper()
	err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, obj)
	if err == nil {
		return true
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get %s/%s: %v", ns, name, err)
	}
	return false
}

func argoObj(kind, ns, name string, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(install.ArgoGVK(kind))
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetLabels(labels)
	return u
}

// A missing hard prerequisite must halt the pipeline at the FIRST phase: the CR
// reports BlockedOnDependencies with a requeue, and no later phase runs: nothing
// (secrets, workload, argo resources) gets provisioned on a cluster that can't
// host it yet.
func TestReconcileHaltsOnMissingHardDependency(t *testing.T) {
	dv := testCR()
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, func(*rest.Config) (deps.Result, error) {
		return deps.Result{MissingHard: []string{"KubeVirt / OpenShift Virtualization"}}, nil
	})

	res := reconcileOnce(t, r, dv)
	if res.RequeueAfter != time.Minute {
		t.Fatalf("RequeueAfter = %v, want 1m", res.RequeueAfter)
	}

	got := getCR(t, c, dv)
	if got.Status.Phase != dotvirtv1alpha1.PhaseBlockedOnDependencies {
		t.Errorf("phase = %q, want %q", got.Status.Phase, dotvirtv1alpha1.PhaseBlockedOnDependencies)
	}
	dep := cond(got, dotvirtv1alpha1.ConditionDependenciesReady)
	if dep == nil || dep.Status != metav1.ConditionFalse || dep.Reason != "MissingPrerequisite" {
		t.Errorf("DependenciesReady = %+v, want False/MissingPrerequisite", dep)
	}
	for _, ct := range []string{
		dotvirtv1alpha1.ConditionWorkloadReady,
		dotvirtv1alpha1.ConditionArgoReady,
		dotvirtv1alpha1.ConditionAvailable,
	} {
		if cond(got, ct) != nil {
			t.Errorf("condition %s set; later phases must not run after the halt", ct)
		}
	}
	var secrets corev1.SecretList
	if err := c.List(context.Background(), &secrets, client.InNamespace(dv.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 0 {
		t.Errorf("blocked reconcile generated %d secrets, want 0", len(secrets.Items))
	}
	if exists(t, c, &appsv1.Deployment{}, dv.Namespace, install.AppName) {
		t.Error("blocked reconcile deployed the workload")
	}
}

// A minimal CR (BYO forge left unconfigured) reconciles end-to-end in one pass:
// every phase applies against the fake API, and the CR lands Ready/Available with
// the finalizer set. This pins the pipeline's happy path, including that the
// server-side applies in workload/argo phases succeed and converge.
func TestReconcileMinimalCRToReady(t *testing.T) {
	dv := testCR()
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)

	if res := reconcileOnce(t, r, dv); res != (ctrl.Result{}) {
		t.Fatalf("result = %+v, want zero (no requeue)", res)
	}

	got := getCR(t, c, dv)
	if got.Status.Phase != dotvirtv1alpha1.PhaseReady {
		t.Errorf("phase = %q, want Ready", got.Status.Phase)
	}
	if got.Status.ObservedGeneration != got.Generation {
		t.Errorf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, got.Generation)
	}
	if !controllerutil.ContainsFinalizer(got, dotvirtFinalizer) {
		t.Error("finalizer missing; cluster-scoped cleanup would never run on delete")
	}
	for ct, want := range map[string]metav1.ConditionStatus{
		dotvirtv1alpha1.ConditionDependenciesReady: metav1.ConditionTrue,
		dotvirtv1alpha1.ConditionWorkloadReady:     metav1.ConditionTrue,
		dotvirtv1alpha1.ConditionArgoReady:         metav1.ConditionTrue,
		dotvirtv1alpha1.ConditionAvailable:         metav1.ConditionTrue,
		// No forge URL: webhook registration is skipped, Argo falls back to its poll.
		dotvirtv1alpha1.ConditionArgoWebhook: metav1.ConditionUnknown,
	} {
		if co := cond(got, ct); co == nil || co.Status != want {
			t.Errorf("condition %s = %+v, want %s", ct, co, want)
		}
	}

	argoNS := platform.Kubernetes.DefaultArgoNamespace()
	if !exists(t, c, &appsv1.Deployment{}, dv.Namespace, install.AppName) {
		t.Error("workload Deployment not applied")
	}
	for _, name := range []string{
		install.SessionSecretName, install.AppsetSecretName,
		install.WebhookSecretName, install.ArgoWebhookSecretName,
	} {
		if !exists(t, c, &corev1.Secret{}, dv.Namespace, name) {
			t.Errorf("generated secret %s missing", name)
		}
	}
	if !exists(t, c, &corev1.Secret{}, argoNS, install.AppsetSecretName) {
		t.Error("appset token not mirrored into the ArgoCD namespace")
	}
	appset := install.ApplicationSet(dv, argoNS)
	if !exists(t, c, argoObj("ApplicationSet", argoNS, appset.GetName(), nil), argoNS, appset.GetName()) {
		t.Error("ApplicationSet not applied")
	}
	var crbs rbacv1.ClusterRoleBindingList
	if err := c.List(context.Background(), &crbs, client.MatchingLabels{"dotvirt.io/instance": dv.Name}); err != nil {
		t.Fatal(err)
	}
	if len(crbs.Items) != 3 {
		t.Errorf("instance-labeled ClusterRoleBindings = %d, want 3", len(crbs.Items))
	}
}

// Generated secrets are create-once: the session key and plugin/webhook tokens
// must survive re-reconciles and operator restarts (a rotation would invalidate
// live sessions and registered webhooks), and a secret already on the cluster is
// never overwritten.
func TestReconcileSecretsCreateOnce(t *testing.T) {
	dv := testCR()
	preseeded := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: install.SessionSecretName, Namespace: dv.Namespace},
		Data:       map[string][]byte{"secret": []byte("preseeded")},
	}
	c := testBuilder(t).WithObjects(dv, preseeded).Build()
	r := newReconciler(c, depsOK)

	reconcileOnce(t, r, dv)
	generated := map[string]string{
		install.SessionSecretName:     "secret",
		install.AppsetSecretName:      "token",
		install.WebhookSecretName:     "secret",
		install.ArgoWebhookSecretName: "secret",
	}
	first := map[string]string{}
	for name, key := range generated {
		var s corev1.Secret
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: dv.Namespace, Name: name}, &s); err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if len(s.Data[key]) == 0 {
			t.Fatalf("secret %s has no %q value", name, key)
		}
		first[name] = string(s.Data[key])
	}
	if first[install.SessionSecretName] != "preseeded" {
		t.Errorf("pre-existing secret was regenerated: %q", first[install.SessionSecretName])
	}

	reconcileOnce(t, r, dv)
	for name, key := range generated {
		var s corev1.Secret
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: dv.Namespace, Name: name}, &s); err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if got := string(s.Data[key]); got != first[name] {
			t.Errorf("secret %s rotated across reconciles: %q -> %q", name, first[name], got)
		}
	}
}

// Deletion cleans exactly the label-tracked resources of THIS instance: its
// ClusterRoleBindings, its ArgoCD-namespace Secrets/ConfigMaps and argoproj.io
// objects. Other instances' and unlabeled resources survive, resources outside the
// cleanup scope (the CR's own namespace, which is ownerRef-GC'd) are untouched,
// and dropping the finalizer releases the CR.
func TestFinalizeDeletesOnlyThisInstance(t *testing.T) {
	now := metav1.Now()
	dv := testCR()
	dv.Finalizers = []string{dotvirtFinalizer}
	dv.DeletionTimestamp = &now

	argoNS := platform.Kubernetes.DefaultArgoNamespace()
	mine := install.Labels(dv.Name)
	other := map[string]string{"dotvirt.io/instance": "other"}

	c := testBuilder(t).WithObjects(
		dv,
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "crb-mine", Labels: mine}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "crb-other", Labels: other}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sec-mine", Namespace: argoNS, Labels: mine}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sec-other", Namespace: argoNS, Labels: other}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm-mine", Namespace: argoNS, Labels: mine}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sec-home", Namespace: dv.Namespace, Labels: mine}},
		argoObj("AppProject", argoNS, "proj-mine", mine),
		argoObj("AppProject", argoNS, "proj-other", other),
		argoObj("Application", argoNS, "app-mine", mine),
		argoObj("ApplicationSet", argoNS, "as-mine", mine),
	).Build()
	r := newReconciler(c, depsOK) // probe must not matter on the deletion path

	if res := reconcileOnce(t, r, dv); res != (ctrl.Result{}) {
		t.Fatalf("result = %+v, want zero", res)
	}

	if exists(t, c, &dotvirtv1alpha1.Dotvirt{}, dv.Namespace, dv.Name) {
		t.Error("CR still present; finalizer was not removed")
	}
	for _, del := range []struct {
		obj client.Object
		ns  string
		nam string
	}{
		{&rbacv1.ClusterRoleBinding{}, "", "crb-mine"},
		{&corev1.Secret{}, argoNS, "sec-mine"},
		{&corev1.ConfigMap{}, argoNS, "cm-mine"},
		{argoObj("AppProject", "", "", nil), argoNS, "proj-mine"},
		{argoObj("Application", "", "", nil), argoNS, "app-mine"},
		{argoObj("ApplicationSet", "", "", nil), argoNS, "as-mine"},
	} {
		if exists(t, c, del.obj, del.ns, del.nam) {
			t.Errorf("%s/%s not cleaned up", del.ns, del.nam)
		}
	}
	for _, kept := range []struct {
		obj client.Object
		ns  string
		nam string
	}{
		{&rbacv1.ClusterRoleBinding{}, "", "crb-other"},
		{&corev1.Secret{}, argoNS, "sec-other"},
		{argoObj("AppProject", "", "", nil), argoNS, "proj-other"},
		// Own-namespace resources are ownerRef-GC'd by Kubernetes, never by cleanup.
		{&corev1.Secret{}, dv.Namespace, "sec-home"},
	} {
		if !exists(t, c, kept.obj, kept.ns, kept.nam) {
			t.Errorf("%s/%s deleted; cleanup must only take this instance's tracked resources", kept.ns, kept.nam)
		}
	}
}

// Cleanup tolerates a cluster the resources already left: NotFound from a
// DeleteAllOf and a missing argoproj.io CRD (NoMatchError, ArgoCD uninstalled
// first) must not wedge deletion; the finalizer still comes off.
func TestFinalizeToleratesNotFoundAndMissingArgoCRD(t *testing.T) {
	now := metav1.Now()
	dv := testCR()
	dv.Finalizers = []string{dotvirtFinalizer}
	dv.DeletionTimestamp = &now

	c := testBuilder(t).WithObjects(dv).WithInterceptorFuncs(interceptor.Funcs{
		DeleteAllOf: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteAllOfOption) error {
			switch obj.(type) {
			case *unstructured.Unstructured:
				gvk := obj.GetObjectKind().GroupVersionKind()
				return &apimeta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}}
			case *corev1.ConfigMap:
				return apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "")
			}
			return cl.DeleteAllOf(ctx, obj, opts...)
		},
	}).Build()
	r := newReconciler(c, depsOK)

	reconcileOnce(t, r, dv)
	if exists(t, c, &dotvirtv1alpha1.Dotvirt{}, dv.Namespace, dv.Name) {
		t.Error("CR still present; tolerated errors must not block the finalizer")
	}
}
