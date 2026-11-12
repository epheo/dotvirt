package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/deps"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

// byoForgeCR is a CR wired to a bring-your-own forge at url, with its
// credentials Secret.
func byoForgeCR(url string) (*dotvirtv1alpha1.Dotvirt, *corev1.Secret) {
	dv := testCR()
	dv.Spec.Forge = dotvirtv1alpha1.ForgeSpec{
		URL:               url,
		CredentialsSecret: "byo-creds",
		PlatformRepo:      url + "/dotvirt/platform.git",
	}
	creds := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "byo-creds", Namespace: dv.Namespace},
		Data:       map[string][]byte{"url": []byte(url), "username": []byte("bot"), "token": []byte("tok")},
	}
	return dv, creds
}

// A fresh install whose forge is down still completes its pass: the operand
// serves without the platform repo, so Available, the phase and the observed
// generation land, and only the requeue tightens until the repo exists.
func TestPlatformRepoFailureNeverBlocksAvailable(t *testing.T) {
	forgeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer forgeDown.Close()
	dv, creds := byoForgeCR(forgeDown.URL)
	c := testBuilder(t).WithObjects(dv, creds).Build()
	r := newReconciler(c, depsOK)

	reconcileOnce(t, r, dv)
	markOperandReady(t, c, dv)
	res := reconcileOnce(t, r, dv)

	got := getCR(t, c, dv)
	if av := cond(got, dotvirtv1alpha1.ConditionAvailable); av == nil || av.Status != metav1.ConditionTrue {
		t.Errorf("Available = %+v, want True: the operand serves without the platform repo", av)
	}
	if got.Status.Phase != dotvirtv1alpha1.PhaseReady || got.Status.ObservedGeneration != got.Generation {
		t.Errorf("phase=%q observedGeneration=%d, want Ready at generation %d", got.Status.Phase, got.Status.ObservedGeneration, got.Generation)
	}
	if repo := cond(got, dotvirtv1alpha1.ConditionForgeRepoReady); repo == nil || repo.Status != metav1.ConditionFalse {
		t.Errorf("ForgeRepoReady = %+v, want False", repo)
	}
	if res.RequeueAfter != settleRetry {
		t.Errorf("RequeueAfter = %v, want %v while the repo is missing", res.RequeueAfter, settleRetry)
	}
}

// A blocking wait rolls up into Available=False naming the phase.
func TestWaitRollsUpIntoAvailable(t *testing.T) {
	dv := testCR()
	r := newReconciler(testBuilder(t).WithObjects(dv).Build(), depsOK)
	r.setCondition(dv, dotvirtv1alpha1.ConditionAvailable, metav1.ConditionTrue, "Reconciled", "")
	dv.Status.Phase = dotvirtv1alpha1.PhaseReady

	r.waitPhase(dv, dotvirtv1alpha1.ConditionForgeReady, "Progressing", "forge starting",
		dotvirtv1alpha1.PhaseProvisioning, time.Minute)
	av := cond(dv, dotvirtv1alpha1.ConditionAvailable)
	if av.Status != metav1.ConditionFalse || av.Reason != "Progressing" || !strings.HasPrefix(av.Message, dotvirtv1alpha1.ConditionForgeReady) {
		t.Errorf("Available = %+v, want False/Progressing naming ForgeReady", av)
	}
	if dv.Status.Phase != dotvirtv1alpha1.PhaseProvisioning {
		t.Errorf("phase = %q, want Provisioning", dv.Status.Phase)
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
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{install.ProjectLabel: "team-a"}}},
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
		// No Application references the shared tier, so it is not rendered.
		{argoObj("AppProject", "", "", nil), "argocd", "dotvirt-tenants"},
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
		{argoObj("AppProject", "", "", nil), "argocd", "dotvirt-p-team-a"},
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
		return deps.Result{MissingHard: []string{"Argo CD"}}, nil
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

// The status write is diffed against the pass's read, so it carries no
// resourceVersion: OLM or a label edit landing between the read and the write
// must not fail the pass with a conflict.
func TestStatusWriteSurvivesConcurrentWriter(t *testing.T) {
	dv := testCR()
	touched := false
	c := testBuilder(t).WithObjects(dv).WithInterceptorFuncs(interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, cl client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if !touched {
				touched = true
				other := &dotvirtv1alpha1.Dotvirt{}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), other); err != nil {
					return err
				}
				other.Labels = map[string]string{"touched": "true"}
				if err := cl.Update(ctx, other); err != nil {
					return err
				}
			}
			return cl.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	}).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	if got := getCR(t, c, dv); got.Status.Phase == "" {
		t.Error("status not written")
	}
}

// failRouteGet fails reads of the named Route; every other read passes through.
func failRouteGet(name string) interceptor.Funcs {
	return interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if u, ok := obj.(*unstructured.Unstructured); ok && u.GroupVersionKind() == install.RouteGVK && key.Name == name {
				return errors.New("apiserver unavailable")
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	}
}

// A failed Route read is not "no host yet": rendering the console without
// DOTVIRT_PUBLIC_URL rolls the Recreate pod and breaks OAuth until the next pass.
func TestRouteReadErrorFailsWorkload(t *testing.T) {
	dv := testCR()
	c := testBuilder(t).WithObjects(dv).WithInterceptorFuncs(failRouteGet(install.AppName)).Build()
	r := newReconciler(c, depsOK)
	r.Platform = platform.OpenShift

	if _, err := r.reconcileWorkload(context.Background(), dv, phaseCtx(r, dv)); err == nil {
		t.Error("reconcileWorkload succeeded on a failed Route read")
	}
	if exists(t, c, &appsv1.Deployment{}, dv.Namespace, install.AppName) {
		t.Error("Deployment rendered without the console host")
	}
}

// The Forgejo pod template carries the ArgoCD host and the admin secret hash; a
// failed read of either fails the phase instead of rendering a blank that rolls
// the pod.
func TestForgeTemplateReadErrorsFailThePhase(t *testing.T) {
	t.Run("ArgoCD Route", func(t *testing.T) {
		dv := managedForgeCR()
		c := testBuilder(t).WithObjects(dv).WithInterceptorFuncs(failRouteGet("openshift-gitops-server")).Build()
		r := newReconciler(c, depsOK)
		r.Platform = platform.OpenShift
		if _, err := r.reconcileForge(context.Background(), dv, phaseCtx(r, dv)); err == nil {
			t.Error("reconcileForge succeeded on a failed ArgoCD Route read")
		}
		if exists(t, c, &appsv1.Deployment{}, dv.Namespace, install.ForgejoServiceName) {
			t.Error("Forgejo Deployment rendered without the ArgoCD host")
		}
	})
	t.Run("admin secret", func(t *testing.T) {
		dv := managedForgeCR()
		reads := 0
		c := testBuilder(t).WithObjects(dv).WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if key.Name == install.ForgejoAdminSecret {
					if reads++; reads > 1 {
						return errors.New("apiserver unavailable")
					}
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).Build()
		r := newReconciler(c, depsOK)
		if _, err := r.reconcileForge(context.Background(), dv, phaseCtx(r, dv)); err == nil {
			t.Error("reconcileForge succeeded on a failed admin secret read")
		}
		if exists(t, c, &appsv1.Deployment{}, dv.Namespace, install.ForgejoServiceName) {
			t.Error("Forgejo Deployment rendered without the admin secret hash")
		}
	})
}

// Every condition and derived field is re-derived from the current spec: a
// switch from a managed forge with a platform repo to a BYO forge without one
// must not leave the managed verdicts behind.
func TestSpecChangeRetiresStaleStatus(t *testing.T) {
	dv := testCR()
	dv.Spec.Forge = dotvirtv1alpha1.ForgeSpec{URL: "https://byo.example", CredentialsSecret: "byo-creds"}
	var seed DotvirtReconciler
	seed.setCondition(dv, dotvirtv1alpha1.ConditionForgeReady, metav1.ConditionTrue, "Ready", "managed Forgejo bootstrapped")
	seed.setCondition(dv, dotvirtv1alpha1.ConditionForgeRepoReady, metav1.ConditionTrue, "Ready", "platform repo present")
	dv.Status.ForgeAdminHint = "managed hint"
	creds := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "byo-creds", Namespace: dv.Namespace}}
	c := testBuilder(t).WithObjects(dv, creds).Build()

	reconcileOnce(t, newReconciler(c, depsOK), dv)
	got := getCR(t, c, dv)
	if fc := cond(got, dotvirtv1alpha1.ConditionForgeReady); fc == nil || fc.Reason != "BringYourOwn" {
		t.Errorf("ForgeReady = %+v, want the BYO verdict", fc)
	}
	if repo := cond(got, dotvirtv1alpha1.ConditionForgeRepoReady); repo != nil {
		t.Errorf("ForgeRepoReady = %+v survived the platform repo's removal", repo)
	}
	if got.Status.ForgeAdminHint != "" {
		t.Errorf("forgeAdminHint = %q survived the switch off managed", got.Status.ForgeAdminHint)
	}
}

// The status diff's base is read uncached: a cache still holding the Ready of
// two passes ago, behind the Provisioning the last pass wrote, would make a
// return to Ready an empty patch and leave Provisioning on the server.
func TestStatusBaseIsReadUncached(t *testing.T) {
	dv := testCR()
	c := testBuilder(t).WithObjects(dv).Build()
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	markOperandReady(t, c, dv)
	reconcileOnce(t, newReconciler(c, depsOK), dv)
	cached := getCR(t, c, dv)

	lastWrite := cached.DeepCopy()
	lastWrite.Status.Phase = dotvirtv1alpha1.PhaseProvisioning
	if err := c.Status().Update(context.Background(), lastWrite); err != nil {
		t.Fatal(err)
	}
	lagging := interceptor.NewClient(c, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if out, ok := obj.(*dotvirtv1alpha1.Dotvirt); ok {
				cached.DeepCopyInto(out)
				return nil
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	})
	r := newReconciler(lagging, depsOK)
	r.APIReader = c
	reconcileOnce(t, r, dv)
	if got := getCR(t, c, dv); got.Status.Phase != dotvirtv1alpha1.PhaseReady {
		t.Errorf("phase = %q, want Ready: the diff ran against a stale base", got.Status.Phase)
	}
}

// Prune and the finalizer delete one object at a time under a delete grant that
// names each object (rbac_markers.go). Every object prune may sweep, across the
// installs that render the most, must be named there and nothing may be deleted
// cluster-wide or by collection: an unnamed object is skipped and left behind.
func TestDeleteGrantCoversEveryRenderedName(t *testing.T) {
	// The tenant AppProjects are named after their projects, so their delete
	// cannot be scoped by name; prune bounds it to this install's labels
	// (TestFinalizeDeletesOnlyThisInstance; as the operator, the envtest deletion
	// subtest). Nothing else may join them.
	unscoped := map[string]bool{"argoproj.io/appprojects": true}
	granted := map[string]bool{}
	for _, role := range readClusterRoles(t, filepath.Join("..", "..", "config", "rbac", "role.yaml")) {
		for _, rule := range role.Rules {
			if slices.Contains(rule.Verbs, "deletecollection") {
				t.Errorf("deletecollection granted on %v", rule.Resources)
			}
			if !slices.Contains(rule.Verbs, "delete") {
				continue
			}
			for _, g := range rule.APIGroups {
				for _, res := range rule.Resources {
					if len(rule.ResourceNames) == 0 {
						if !unscoped[g+"/"+res] {
							t.Errorf("delete on %s/%s is not scoped by name", g, res)
						}
						granted[g+"/"+res+"/*"] = true
					}
					for _, name := range rule.ResourceNames {
						granted[g+"/"+res+"/"+name] = true
					}
				}
			}
		}
	}
	scheme := testScheme(t)
	scopes := map[schema.GroupKind]pruneScope{}
	for _, sc := range pruneScopes() {
		gvk, err := apiutil.GVKForObject(sc.list(), scheme)
		if err != nil {
			t.Fatal(err)
		}
		scopes[schema.GroupKind{Group: gvk.Group, Kind: strings.TrimSuffix(gvk.Kind, "List")}] = sc
	}

	forgeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer forgeDown.Close()
	byo, creds := byoForgeCR(forgeDown.URL)
	withIngress := managedForgeCR()
	withIngress.Spec.Ingress.Host = "dotvirt.example"
	for _, pass := range []struct {
		name     string
		dv       *dotvirtv1alpha1.Dotvirt
		objs     []client.Object
		platform platform.Platform
		dryRun   bool
	}{
		// Real: the appset mirror and repo-creds Secrets render outside dry-run only.
		{"BYO forge on OpenShift", byo, []client.Object{creds}, platform.OpenShift, false},
		{"managed forge on OpenShift", managedForgeCR(), nil, platform.OpenShift, true},
		{"managed forge behind Ingresses", withIngress, nil, platform.Kubernetes, true},
	} {
		var applied []objKey
		tenant := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{install.ProjectLabel: "team-a"}}}
		c := testBuilder(t).WithObjects(append(pass.objs, pass.dv, tenant)...).WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				gvk, err := apiutil.GVKForObject(obj, cl.Scheme())
				if err != nil {
					return err
				}
				applied = append(applied, objKey{gvk.GroupKind(), obj.GetNamespace(), obj.GetName()})
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
		r := newReconciler(c, depsOK)
		r.Platform, r.DryRun = pass.platform, pass.dryRun
		reconcileOnce(t, r, pass.dv)
		for _, k := range applied {
			sc, prunable := scopes[k.gk]
			if !prunable || (sc.where == writtenNS && k.namespace == pass.dv.Namespace) {
				continue
			}
			resource, _ := apimeta.UnsafeGuessKindToResource(k.gk.WithVersion(""))
			gr := k.gk.Group + "/" + resource.Resource
			if !granted[gr+"/"+k.name] && !granted[gr+"/*"] {
				t.Errorf("%s: %s %s/%s is rendered, but the delete grant does not name it", pass.name, k.gk.Kind, k.namespace, k.name)
			}
		}
	}
}

// The operator patches its exposures and network policies by name only: an
// unscoped patch would let it rewrite any route or policy in the cluster. Every
// one it renders must be named in the grant, or its apply is refused.
func TestPatchGrantNamesEveryRenderedExposure(t *testing.T) {
	scoped := map[string]bool{
		"route.openshift.io/routes":            true,
		"gateway.networking.k8s.io/httproutes": true,
		"networking.k8s.io/ingresses":          true,
		"networking.k8s.io/networkpolicies":    true,
	}
	granted := map[string]bool{}
	for _, role := range readClusterRoles(t, filepath.Join("..", "..", "config", "rbac", "role.yaml")) {
		for _, rule := range role.Rules {
			if !slices.Contains(rule.Verbs, "patch") && !slices.Contains(rule.Verbs, "update") {
				continue
			}
			for _, g := range rule.APIGroups {
				for _, res := range rule.Resources {
					if !scoped[g+"/"+res] {
						continue
					}
					if len(rule.ResourceNames) == 0 {
						t.Errorf("patch on %s/%s is not scoped by name", g, res)
					}
					for _, name := range rule.ResourceNames {
						granted[g+"/"+res+"/"+name] = true
					}
				}
			}
		}
	}
	withIngress := managedForgeCR()
	withIngress.Spec.Ingress.Host = "dotvirt.example"
	withGateway := managedForgeCR()
	withGateway.Spec.Ingress = dotvirtv1alpha1.IngressSpec{Host: "dotvirt.example", Gateway: &dotvirtv1alpha1.GatewayRef{Name: "edge"}}
	seen := map[string]bool{}
	for _, pass := range []struct {
		name     string
		dv       *dotvirtv1alpha1.Dotvirt
		platform platform.Platform
	}{
		{"Routes", managedForgeCR(), platform.OpenShift},
		{"Ingresses", withIngress, platform.Kubernetes},
		{"HTTPRoutes", withGateway, platform.Kubernetes},
	} {
		var applied []objKey
		c := testBuilder(t).WithObjects(pass.dv).WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				gvk, err := apiutil.GVKForObject(obj, cl.Scheme())
				if err != nil {
					return err
				}
				applied = append(applied, objKey{gvk.GroupKind(), obj.GetNamespace(), obj.GetName()})
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
		r := newReconciler(c, depsOK)
		r.Platform, r.DryRun = pass.platform, true
		reconcileOnce(t, r, pass.dv)
		for _, k := range applied {
			resource, _ := apimeta.UnsafeGuessKindToResource(k.gk.WithVersion(""))
			gr := k.gk.Group + "/" + resource.Resource
			if !scoped[gr] {
				continue
			}
			seen[gr] = true
			if !granted[gr+"/"+k.name] {
				t.Errorf("%s: %s %s/%s is rendered, but the patch grant does not name it", pass.name, k.gk.Kind, k.namespace, k.name)
			}
		}
	}
	for gr := range scoped {
		if !seen[gr] {
			t.Errorf("no pass renders %s, so its grant goes unchecked", gr)
		}
	}
}
