package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/deps"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

// The platform-repo retry passes phase "": the operand still serves, so a
// serving install stays Available. Every other blocking wait or failure rolls
// up into Available=False.
func TestAvailableRollUpSparesPlatformRepoRetry(t *testing.T) {
	dv := testCR()
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)
	r.setCondition(dv, dotvirtv1alpha1.ConditionAvailable, metav1.ConditionTrue, "Reconciled", "")
	dv.Status.Phase = dotvirtv1alpha1.PhaseReady

	if _, err := r.waitPhase(context.Background(), dv, dotvirtv1alpha1.ConditionForgeRepoReady, "Error", "forge down", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	if av := cond(dv, dotvirtv1alpha1.ConditionAvailable); av.Status != metav1.ConditionTrue || dv.Status.Phase != dotvirtv1alpha1.PhaseReady {
		t.Errorf("platform-repo retry regressed a serving install: Available=%+v phase=%q", av, dv.Status.Phase)
	}

	if _, err := r.waitPhase(context.Background(), dv, dotvirtv1alpha1.ConditionForgeReady, "Progressing", "forge starting",
		dotvirtv1alpha1.PhaseProvisioning, time.Minute); err != nil {
		t.Fatal(err)
	}
	av := cond(dv, dotvirtv1alpha1.ConditionAvailable)
	if av.Status != metav1.ConditionFalse || av.Reason != "Progressing" || !strings.HasPrefix(av.Message, dotvirtv1alpha1.ConditionForgeReady) {
		t.Errorf("Available = %+v, want False/Progressing naming ForgeReady", av)
	}
}

// Prune deletes exactly this install's objects the pass no longer renders: a
// switched exposure, a managed forge turned off, the platform tier with no
// platform repo, everything in a former ArgoCD namespace, a name an older
// version rendered. State (Secrets, PVCs, CA copies) in the install namespace,
// unlabelled objects and a same-named install elsewhere are never touched.
func TestPruneDeletesOnlyUnrenderedInstanceObjects(t *testing.T) {
	dv := testCR()
	mine := install.Labels(dv)
	legacy := map[string]string{install.InstanceLabel: dv.Name}
	sameNameElsewhere := map[string]string{install.InstanceLabel: dv.Name, install.InstanceNamespaceLabel: "elsewhere"}
	oldArgo := "argo-old"

	c := testBuilder(t).WithObjects(
		dv,
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "stale-exposure", Namespace: dv.Namespace, Labels: mine}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: install.ForgejoServiceName, Namespace: dv.Namespace, Labels: mine}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "hand-made", Namespace: dv.Namespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: install.ForgejoAdminSecret, Namespace: dv.Namespace, Labels: mine}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "dotvirt-forgejo-data", Namespace: dv.Namespace, Labels: mine}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: install.IngressCAConfigMap, Namespace: dv.Namespace, Labels: mine}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "dotvirt-renamed", Labels: legacy}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "dotvirt", Labels: legacy}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "other-install", Labels: sameNameElsewhere}},
		argoObj("AppProject", "argocd", "dotvirt-platform", mine),
		argoObj("AppProject", oldArgo, "dotvirt-tenants", mine),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: install.AppsetSecretName, Namespace: oldArgo, Labels: mine}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "dotvirt-appset-plugin", Namespace: oldArgo, Labels: legacy}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: oldArgo, Labels: sameNameElsewhere}},
		// An admin's `get -o yaml | apply -n` copy keeps the labels.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "dotvirt-forge", Namespace: "admin-copy", Labels: mine}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "dotvirt-appset-plugin", Namespace: "admin-copy", Labels: mine}},
	).Build()
	r := newReconciler(c, depsOK)
	reconcileOnce(t, r, dv)

	type ref struct {
		obj      client.Object
		ns, name string
	}
	for _, gone := range []ref{
		{&networkingv1.Ingress{}, dv.Namespace, "stale-exposure"},
		{&corev1.Service{}, dv.Namespace, install.ForgejoServiceName},
		{&rbacv1.ClusterRoleBinding{}, "", "dotvirt-renamed"},
		{argoObj("AppProject", "", "", nil), "argocd", "dotvirt-platform"},
		{argoObj("AppProject", "", "", nil), oldArgo, "dotvirt-tenants"},
		{&corev1.Secret{}, oldArgo, install.AppsetSecretName},
		{&corev1.ConfigMap{}, oldArgo, "dotvirt-appset-plugin"},
	} {
		if exists(t, c, gone.obj, gone.ns, gone.name) {
			t.Errorf("%s/%s survived prune", gone.ns, gone.name)
		}
	}
	for _, kept := range []ref{
		{&corev1.Service{}, dv.Namespace, "hand-made"},
		{&corev1.Service{}, dv.Namespace, install.AppName},
		{&corev1.Secret{}, dv.Namespace, install.ForgejoAdminSecret},
		{&corev1.PersistentVolumeClaim{}, dv.Namespace, "dotvirt-forgejo-data"},
		{&corev1.ConfigMap{}, dv.Namespace, install.IngressCAConfigMap},
		{&rbacv1.ClusterRoleBinding{}, "", "other-install"},
		{&corev1.Secret{}, oldArgo, "foreign"},
		{&corev1.Secret{}, "admin-copy", "dotvirt-forge"},
		{&corev1.ConfigMap{}, "admin-copy", "dotvirt-appset-plugin"},
		{argoObj("AppProject", "", "", nil), "argocd", "dotvirt-tenants"},
		{argoObj("ApplicationSet", "", "", nil), "argocd", "dotvirt-projects"},
	} {
		if !exists(t, c, kept.obj, kept.ns, kept.name) {
			t.Errorf("%s/%s pruned; it is rendered, state, or not this install's", kept.ns, kept.name)
		}
	}
	var crb rbacv1.ClusterRoleBinding
	if !exists(t, c, &crb, "", "dotvirt") || crb.Labels[install.InstanceNamespaceLabel] != dv.Namespace {
		t.Errorf("legacy-labelled rendered binding not kept and relabelled: %v", crb.Labels)
	}
}

// A halted pass has not rendered its later phases, so it must not prune them.
func TestPruneSkippedWhenPassHalts(t *testing.T) {
	dv := testCR()
	stale := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "stale", Namespace: dv.Namespace, Labels: install.Labels(dv)}}
	c := testBuilder(t).WithObjects(dv, stale).Build()
	r := newReconciler(c, func(*rest.Config) (deps.Result, error) {
		return deps.Result{MissingHard: []string{"KubeVirt"}}, nil
	})
	reconcileOnce(t, r, dv)
	if !exists(t, c, &networkingv1.Ingress{}, dv.Namespace, "stale") {
		t.Error("a halted pass pruned")
	}
}

// Deletion takes legacy name-only objects too, but never those of a same-named
// install in another namespace.
func TestFinalizeTakesLegacyLabels(t *testing.T) {
	now := metav1.Now()
	dv := testCR()
	dv.Finalizers = []string{dotvirtFinalizer}
	dv.DeletionTimestamp = &now
	c := testBuilder(t).WithObjects(
		dv,
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Labels: map[string]string{install.InstanceLabel: dv.Name}}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Labels: map[string]string{
			install.InstanceLabel: dv.Name, install.InstanceNamespaceLabel: "elsewhere",
		}}},
	).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if exists(t, c, &rbacv1.ClusterRoleBinding{}, "", "legacy") {
		t.Error("legacy-labelled binding survived deletion")
	}
	if !exists(t, c, &rbacv1.ClusterRoleBinding{}, "", "elsewhere") {
		t.Error("deletion took a same-named install's binding")
	}
}

// Two CRs may share a name across namespaces; the mirror check must see them
// as different installs.
func TestMirrorRefusesSameNameOtherNamespace(t *testing.T) {
	dv := testCR()
	taken := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: install.AppsetSecretName, Namespace: "argocd", Labels: map[string]string{
		install.InstanceLabel: dv.Name, install.InstanceNamespaceLabel: "elsewhere",
	}}}
	src := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: install.AppsetSecretName, Namespace: dv.Namespace}, Data: map[string][]byte{"token": []byte("t")}}
	c := testBuilder(t).WithObjects(dv, taken, src).Build()
	r := newReconciler(c, depsOK)
	err := r.mirrorAppsetToken(context.Background(), dv, "argocd")
	if err == nil || !strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("mirrorAppsetToken = %v, want a conflict naming the other install", err)
	}
}

// The policies are rendered on OpenShift only: elsewhere the ingress
// controller's namespace is unknown and a guess would cut the console off.
func TestNetworkPolicyOpenShiftOnly(t *testing.T) {
	for _, plat := range []platform.Platform{platform.OpenShift, platform.Kubernetes} {
		dv := testCR()
		dv.Spec.Ingress.Host = "dotvirt.example"
		c := testBuilder(t).WithObjects(dv).Build()
		r := newReconciler(c, depsOK)
		r.Platform = plat
		r.OperatorNamespace = "dotvirt-operator"
		if _, err := r.reconcileWorkload(context.Background(), dv, phaseCtx(r, dv)); err != nil {
			t.Fatalf("%s: reconcileWorkload: %v", plat, err)
		}
		got := exists(t, c, &networkingv1.NetworkPolicy{}, dv.Namespace, install.AppName)
		if got != (plat == platform.OpenShift) {
			t.Errorf("%s: NetworkPolicy present = %v", plat, got)
		}
	}
}

// A transient read of the forge credential is an error, not "no credential":
// read as absent, prune would delete ArgoCD's live repo-creds.
func TestRepoCredsReadErrorIsNotAbsence(t *testing.T) {
	dv := testCR()
	dv.Spec.Forge.URL = "https://forge.example"
	c := testBuilder(t).WithObjects(dv).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return errors.New("apiserver unavailable")
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	}).Build()
	r := newReconciler(c, depsOK)
	if obj, err := r.repoCreds(context.Background(), dv, "argocd"); err == nil {
		t.Errorf("repoCreds = (%v, nil), want the read error", obj)
	}
}

// Reconciles fire on every labelled object event; prune's uncached LISTs run
// only when the render changed or the drift timer is due.
func TestPruneRunsOnRenderChangeOrTimer(t *testing.T) {
	dv := testCR()
	lists := 0
	c := testBuilder(t).WithObjects(dv).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*rbacv1.ClusterRoleBindingList); ok {
				lists++
			}
			return cl.List(ctx, list, opts...)
		},
	}).Build()
	r := newReconciler(c, depsOK)
	now := time.Now()
	r.now = func() time.Time { return now }

	reconcileOnce(t, r, dv)
	if lists != 1 {
		t.Fatalf("first pass pruned %d times, want 1", lists)
	}
	reconcileOnce(t, r, dv)
	if lists != 1 {
		t.Errorf("unchanged render pruned again before the timer")
	}

	fresh := getCR(t, c, dv)
	fresh.Spec.Ingress.Host = "dotvirt.example"
	if err := c.Update(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, dv)
	if lists != 2 {
		t.Errorf("changed render (new Ingress) did not prune: %d", lists)
	}

	now = now.Add(driftResync)
	reconcileOnce(t, r, dv)
	if lists != 3 {
		t.Errorf("drift timer did not prune")
	}
}
