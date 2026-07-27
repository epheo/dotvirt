package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/deps"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/forge"
)

// DotvirtReconciler provisions a dotvirt install from a Dotvirt resource (see the
// api package doc for what an install comprises). dotvirt's RUNTIME still owns
// nothing - this operator is the install-time provisioner, so it holds the
// privileged install RBAC and forge-admin credential the app never touches.
// Its RBAC markers live in rbac_markers.go.
type DotvirtReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Config   *rest.Config      // discovery for the dependency probe
	Platform platform.Platform // detected once at startup (cmd/main.go)
	DryRun   bool              // -dry-run: validate via server-side dry-run apply; persist nothing

	// probe is a test seam (not config): deps.Probe needs a live discovery
	// endpoint, so tests stub it. nil = the real probe.
	probe func(*rest.Config) (deps.Result, error)

	// forgeAPIBase is a test seam: the managed-Forgejo bootstrap talks to the forge
	// over its in-cluster Service URL, which a unit test redirects to an httptest
	// server. nil = the real Service URL.
	forgeAPIBase func(*dotvirtv1alpha1.Dotvirt) string
}

// dotvirtFinalizer guards cleanup of the cluster-scoped + ArgoCD-namespace
// resources, which a namespaced CR can't garbage-collect via ownerReferences.
const dotvirtFinalizer = "dotvirt.io/finalizer"

// reconcilePhase is one step of the install pipeline. It owns one status condition,
// and halts the reconcile by returning a non-nil result (carrying any requeue) or
// an error; (nil, nil) hands off to the next phase.
type reconcilePhase func(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error)

// Reconcile drives the install in order, recording a status condition per step so a
// stuck install is legible from `kubectl get dotvirt` / `describe`.
func (r *DotvirtReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var dv dotvirtv1alpha1.Dotvirt
	if err := r.Get(ctx, req.NamespacedName, &dv); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("reconciling dotvirt install", "platform", r.Platform)

	if !dv.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &dv)
	}
	r.normalizeSpec(&dv)
	// Ensure the finalizer is present before provisioning anything cluster-scoped.
	// Skipped under -dry-run so a validation run mutates nothing (and the CR stays
	// freely deletable, since the finalizer would otherwise gate its removal).
	if !r.DryRun && controllerutil.AddFinalizer(&dv, dotvirtFinalizer) {
		if err := r.Update(ctx, &dv); err != nil {
			return ctrl.Result{}, err
		}
	}

	// The install pipeline, in dependency order. A phase that halts (requeue or
	// error) has already recorded why; a completed pass falls through to the Ready
	// status write below.
	for _, phase := range []reconcilePhase{
		r.reconcileDependencies,
		r.reconcileForge,
		r.reconcileSecrets,
		r.reconcileWorkload,
		r.reconcileArgo,
		r.reconcileArgoWebhook,
		r.reconcileDotvirtWebhook,
		// Last: its failure requeues, and nothing above depends on the repo.
		r.reconcilePlatformRepo,
	} {
		res, err := phase(ctx, &dv)
		if err != nil {
			return ctrl.Result{}, err
		}
		if res != nil {
			return *res, nil
		}
	}

	if r.DryRun {
		log.Info("dry-run complete: all rendered resources accepted by the API server (nothing persisted)")
	}
	r.setCondition(&dv, dotvirtv1alpha1.ConditionAvailable, metav1.ConditionTrue, "Reconciled", "install reconciled")
	dv.Status.Phase = dotvirtv1alpha1.PhaseReady
	dv.Status.ObservedGeneration = dv.Generation
	if err := r.writeStatus(ctx, &dv); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// normalizeSpec derives the EFFECTIVE in-memory spec the whole pipeline consumes.
// Never persisted: after the finalizer add the reconcile writes only /status, so
// these mutations stay in-process (a regression test pins the stored spec). The
// forge and exposure phases fill their resolved hosts into the spec the same way.
func (r *DotvirtReconciler) normalizeSpec(dv *dotvirtv1alpha1.Dotvirt) {
	// SSO is OpenShift-only: the app's oauth flow runs against the cluster oauth server.
	if dv.Spec.Auth.OpenShiftSSO && r.Platform != platform.OpenShift {
		dv.Spec.Auth.OpenShiftSSO = false
	}
}

// reconcileDependencies gates on the hard prerequisites: ArgoCD + KubeVirt are
// PREREQUISITES we never install; if either is absent, record why and requeue (the
// admin may install the prereq operator). OVN-K/NMState/CDI are soft - note them
// and proceed.
func (r *DotvirtReconciler) reconcileDependencies(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	probe := r.probe
	if probe == nil {
		probe = deps.Probe
	}
	depRes, err := probe(r.Config)
	if err != nil {
		// A failed probe proves nothing about the prerequisites; proceeding would
		// report "all dependencies present" from the zero result.
		logf.FromContext(ctx).Error(err, "dependency probe failed")
		dv.Status.ObservedGeneration = dv.Generation
		return r.waitPhase(ctx, dv, dotvirtv1alpha1.ConditionDependenciesReady, "ProbeFailed",
			err.Error(), dotvirtv1alpha1.PhaseBlockedOnDependencies, time.Minute)
	}
	if len(depRes.MissingHard) > 0 {
		dv.Status.ObservedGeneration = dv.Generation
		return r.waitPhase(ctx, dv, dotvirtv1alpha1.ConditionDependenciesReady, "MissingPrerequisite",
			depRes.Summary(), dotvirtv1alpha1.PhaseBlockedOnDependencies, time.Minute)
	}
	r.setCondition(dv, dotvirtv1alpha1.ConditionDependenciesReady, metav1.ConditionTrue, "Satisfied", depRes.Summary())
	return nil, nil
}

// apply server-side-applies obj honoring -dry-run. Every apply in this package
// goes through here (or applyOwned) so no call site can pass a literal that
// diverges from r.DryRun. SSA is the norm for anything the operator owns or
// converges; Get+Create (ensureSecret) is reserved for create-once generated
// values; mirrorAppsetToken hand-rolls its convergence to enforce the
// one-ArgoCD-namespace-per-install guard.
func (r *DotvirtReconciler) apply(ctx context.Context, obj client.Object) error {
	return install.Apply(ctx, r.Client, obj, r.DryRun)
}

// applyOwned owner-references each object to dv and applies it, stopping at the
// first error. Only for dotvirt-namespace objects (cluster-scoped and foreign-
// namespace ones cannot carry the ownerRef; they go through apply and the
// finalizer cleans them up).
func (r *DotvirtReconciler) applyOwned(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, objs ...client.Object) error {
	for _, obj := range objs {
		if err := controllerutil.SetControllerReference(dv, obj, r.Scheme); err != nil {
			return err
		}
		if err := r.apply(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

// secret reads one namespaced Secret.
func (r *DotvirtReconciler) secret(ctx context.Context, ns, name string) (*corev1.Secret, error) {
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// dryRunSkip records the standard condition for a phase whose real work mutates
// state (the forge, argocd-secret) that server-side dry-run cannot model.
func (r *DotvirtReconciler) dryRunSkip(dv *dotvirtv1alpha1.Dotvirt, condType, what string) {
	r.setCondition(dv, condType, metav1.ConditionUnknown, "DryRun", "skipped "+what+" in dry-run")
}

// forgeClient reads the install's forge credential (ForgeSecretName - the
// admin-supplied BYO secret, or the one the managed-Forgejo bootstrap minted) and
// builds the app's shared forge client (pkg/forge) scoped to the platform repo.
func (r *DotvirtReconciler) forgeClient(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*forge.Client, error) {
	name := install.ForgeSecretName(dv)
	s, err := r.secret(ctx, dv.Namespace, name)
	if err != nil {
		return nil, fmt.Errorf("read forge credentials %q: %w", name, err)
	}
	token := string(s.Data["token"])
	if dv.Spec.Forge.URL == "" || token == "" {
		return nil, fmt.Errorf("forge url (spec.forge.url) and a credential token (%s/token) are required", name)
	}
	// A managed forge is reached over its in-cluster Service (the operator pod may not
	// route to the external Route); owner/repo still parse from the external platform
	// repo URL, so only the base is re-homed.
	base := dv.Spec.Forge.URL
	if dv.Spec.Forge.Managed {
		base = r.managedForgeAPIBase(dv)
	}
	c := forge.NewFactory(base, token, dv.Spec.Forge.InsecureTLS).For(dv.Spec.Forge.PlatformRepo)
	if c == nil {
		return nil, fmt.Errorf("cannot parse platform repo URL %q", dv.Spec.Forge.PlatformRepo)
	}
	return c, nil
}

// routeHost reads the spec.host an OpenShift Route carries: the explicit host, or the
// one the router assigned to a hostless Route. Empty when the Route is absent or its
// host isn't assigned yet. Unstructured so the module needs no openshift/api dep.
func (r *DotvirtReconciler) routeHost(ctx context.Context, ns, name string) string {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(install.RouteGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, route); err != nil {
		return ""
	}
	host, _, _ := unstructured.NestedString(route.Object, "spec", "host")
	return host
}

// argoServerURL resolves the externally reachable ArgoCD base URL: the spec
// override, else the OpenShift GitOps server Route, else "" (caller falls back to
// Argo's poll).
func (r *DotvirtReconciler) argoServerURL(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) string {
	if dv.Spec.ArgoCD.ServerURL != "" {
		return dv.Spec.ArgoCD.ServerURL
	}
	if host := r.routeHost(ctx, argoNS, "openshift-gitops-server"); host != "" {
		return "https://" + host
	}
	return ""
}

// argoTarget resolves the ArgoCD namespace + controller ServiceAccount from the
// spec, defaulting to the platform's conventional install (OpenShift GitOps vs
// community Argo CD).
func (r *DotvirtReconciler) argoTarget(dv *dotvirtv1alpha1.Dotvirt) (ns, sa string) {
	ns, sa = dv.Spec.ArgoCD.Namespace, dv.Spec.ArgoCD.ControllerServiceAccount
	if ns == "" {
		if r.Platform == platform.OpenShift {
			ns = "openshift-gitops"
		} else {
			ns = "argocd"
		}
	}
	if sa == "" {
		if r.Platform == platform.OpenShift {
			sa = "openshift-gitops-argocd-application-controller"
		} else {
			sa = "argocd-application-controller"
		}
	}
	return ns, sa
}

func (r *DotvirtReconciler) setCondition(dv *dotvirtv1alpha1.Dotvirt, condType string, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&dv.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: dv.Generation,
	})
}

// failPhase records a failure condition + the Provisioning phase (best-effort status
// write) and returns the original error so Reconcile requeues on it.
func (r *DotvirtReconciler) failPhase(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, condType, reason string, err error) error {
	r.setCondition(dv, condType, metav1.ConditionFalse, reason, err.Error())
	dv.Status.Phase = dotvirtv1alpha1.PhaseProvisioning
	if uerr := r.writeStatus(ctx, dv); uerr != nil {
		logf.FromContext(ctx).Error(uerr, "status update failed", "phase", dotvirtv1alpha1.PhaseProvisioning)
	}
	return err
}

// waitPhase is failPhase's no-error twin for EXPECTED waits: record the
// not-ready condition + phase, persist status, and hand back the halt result.
// requeue 0 halts without a retry timer (the wait clears via a watch event).
// phase "" leaves Status.Phase untouched (a late retry must not regress a
// Ready install to Provisioning).
func (r *DotvirtReconciler) waitPhase(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, condType, reason, msg, phase string, requeue time.Duration) (*ctrl.Result, error) {
	r.setCondition(dv, condType, metav1.ConditionFalse, reason, msg)
	if phase != "" {
		dv.Status.Phase = phase
	}
	if err := r.writeStatus(ctx, dv); err != nil {
		return nil, err
	}
	if requeue == 0 {
		return &ctrl.Result{}, nil
	}
	return &ctrl.Result{RequeueAfter: requeue}, nil
}

// SetupWithManager registers the reconciler; r.Platform must already be set
// (main.go detects it, failing startup rather than guessing).
func (r *DotvirtReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Watch only the owned kinds the RBAC already lets the cache list: a deleted or
	// drifted Deployment/Secret re-reconciles promptly. Services, SAs, PVCs and
	// Ingresses stay create-only by design (least-privilege RBAC); their drift heals
	// on the next CR-driven reconcile or the manager resync.
	b := ctrl.NewControllerManagedBy(mgr).
		For(&dotvirtv1alpha1.Dotvirt{}).
		Named("dotvirt").
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Secret{})
	// Routes exist only where the route API does, so gate the watch on the same
	// platform detection that gates rendering them; on vanilla Kubernetes the
	// informer could never sync. Unstructured because the module carries no
	// openshift/api dependency (install renders Routes unstructured too).
	if r.Platform == platform.OpenShift {
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(install.RouteGVK)
		b = b.Owns(route)
	}
	return b.Complete(r)
}

// writeStatus persists the derived status as a MERGE PATCH, not an Update: OLM and
// the status informer touch the object between our read and write often enough that
// resourceVersion'd updates spray "object has been modified" requeue noise into the
// log, which reads as a broken install to anyone skimming it. A merge patch carries
// no resourceVersion, and every status field (conditions included) is derived and
// dotvirt-owned, so replace-wholesale semantics are exact.
func (r *DotvirtReconciler) writeStatus(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) error {
	return r.Status().Patch(ctx, dv, client.Merge)
}
