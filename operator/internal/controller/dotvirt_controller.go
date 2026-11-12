package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/deps"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/forge"
	"github.com/epheo/dotvirt/pkg/tenancy"
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
	// ClusterVersioned: a full OpenShift (platform.ServesClusterVersion), whose
	// own RBAC policy grants signed-in users the storage catalog; the operator
	// never binds it there.
	ClusterVersioned bool
	DryRun           bool // -dry-run: validate via server-side dry-run apply; persist nothing
	// APIReader bypasses the cache: the pass's read of the CR and prune's
	// cluster-wide lists.
	APIReader client.Reader
	// OperatorNamespace is admitted by the NetworkPolicies; "" when unknown.
	OperatorNamespace string

	// probe is a test seam (not config): deps.Probe needs a live discovery
	// endpoint, so tests stub it. nil = the real probe.
	probe func(*rest.Config) (deps.Result, error)

	// forgeAPIBase is a test seam: the managed-Forgejo bootstrap talks to the forge
	// over its in-cluster Service URL, which a unit test redirects to an httptest
	// server. nil = the real Service URL.
	forgeAPIBase func(*dotvirtv1alpha1.Dotvirt) string

	// privatized holds the managed installs whose forge repos this process
	// already made private (privatizeOnce).
	privatized sync.Map

	pruneState
	syncCache
}

// driftResync bounds how long out-of-band drift on an unwatched kind (Services,
// AppProjects, bindings) survives without a CR or owned-object event.
const driftResync = 10 * time.Minute

// settleRetry replaces driftResync while a state no watch reports on is
// unsettled: the platform repo missing, or Argo not ready yet.
const settleRetry = time.Minute

// dotvirtFinalizer guards cleanup of the cluster-scoped + ArgoCD-namespace
// resources, which a namespaced CR can't garbage-collect via ownerReferences.
const dotvirtFinalizer = "dotvirt.io/finalizer"

// reconcilePhase is one step of the install pipeline. It owns one status
// condition: any error it returns is recorded there (recordFailure), a non-nil
// result halts the reconcile after a wait the phase already recorded, and
// (nil, nil) hands off to the next phase.
type reconcilePhase struct {
	cond string
	run  func(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (*ctrl.Result, error)
	// dryRunSkip names the work a -dry-run pass skips instead of running the
	// phase: forge and Secret mutations server-side dry-run cannot model. ""
	// runs the phase, which then validates its applies as dry-run.
	dryRunSkip string
}

// Reconcile drives the install in order, recording a status condition per step so a
// stuck install is legible from `kubectl get dotvirt` / `describe`. Phases change
// status in memory only; the pass persists it once, whichever way it ends.
func (r *DotvirtReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Uncached: the status write is a diff against this read, and the cache can
	// lag the previous pass's own write. Diffed against that stale base, a field
	// flipping back (Ready after a Provisioning the cache never saw) would drop
	// out of the patch and stay wrong on the server.
	var dv dotvirtv1alpha1.Dotvirt
	if err := r.reader().Get(ctx, req.NamespacedName, &dv); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("reconciling dotvirt install", "platform", r.Platform)

	if !dv.DeletionTimestamp.IsZero() {
		base := dv.DeepCopy()
		err := r.finalize(ctx, &dv)
		if err != nil {
			// A CR stuck terminating says why.
			r.setCondition(&dv, dotvirtv1alpha1.ConditionAvailable, metav1.ConditionFalse, "CleanupFailed", "deleting the install: "+err.Error())
			if werr := r.writeStatus(ctx, &dv, base); werr != nil {
				log.Error(werr, "status update failed")
			}
		}
		return ctrl.Result{}, err
	}
	base := dv.DeepCopy()
	var res ctrl.Result
	var err error
	// One install per cluster: the ClusterRoleBindings it provisions carry fixed
	// cluster-scoped names, so two CRs would rewrite each other's subjects on every
	// reconcile and each finalizer would delete the other's bindings. The oldest CR
	// wins; a later one halts here, before the finalizer, so it stays freely
	// deletable and owns nothing. The timer matters: the older CR's deletion fires
	// no event for this one.
	if other, found, lerr := r.olderInstall(ctx, &dv); lerr != nil {
		return ctrl.Result{}, lerr
	} else if found {
		msg := fmt.Sprintf("another dotvirt install exists at %s/%s and owns this cluster's bindings; delete one of the two",
			other.Namespace, other.Name)
		res = *r.waitPhase(&dv, dotvirtv1alpha1.ConditionAvailable, "Conflict", msg,
			dotvirtv1alpha1.PhaseProvisioning, time.Minute)
	} else {
		// Ensure the finalizer is present before provisioning anything cluster-scoped.
		// Skipped under -dry-run so a validation run mutates nothing (and the CR stays
		// freely deletable, since the finalizer would otherwise gate its removal).
		if !r.DryRun && controllerutil.AddFinalizer(&dv, dotvirtFinalizer) {
			if err := r.Update(ctx, &dv); err != nil {
				return ctrl.Result{}, err
			}
			// The Update moved resourceVersion; a stale base would put it in the
			// status patch and turn it into an optimistic lock.
			base = dv.DeepCopy()
		}
		// Only after the finalizer add, the reconcile's one spec write: the
		// effective spec the phases fill in must never reach the stored CR.
		res, err = r.provision(ctx, req.NamespacedName, &dv)
	}
	if werr := r.writeStatus(ctx, &dv, base); werr != nil {
		if err == nil {
			return ctrl.Result{}, werr
		}
		log.Error(werr, "status update failed")
	}
	return res, err
}

// provision runs the install pipeline over the effective spec.
func (r *DotvirtReconciler) provision(ctx context.Context, key types.NamespacedName, dv *dotvirtv1alpha1.Dotvirt) (ctrl.Result, error) {
	rc := r.newReconcileCtx(ctx, dv)
	ctx, rendered := withRenderSet(ctx)

	// The install pipeline, in dependency order. A phase that halts on a wait has
	// already recorded why; one that fails is recorded here, so no error is
	// invisible in status; a completed pass falls through to the Ready roll-up
	// below.
	for _, phase := range []reconcilePhase{
		{dotvirtv1alpha1.ConditionDependenciesReady, r.reconcileDependencies, ""},
		{dotvirtv1alpha1.ConditionForgeReady, r.reconcileForge, ""},
		{dotvirtv1alpha1.ConditionSecretsReady, r.reconcileSecrets, "secret generation"},
		{dotvirtv1alpha1.ConditionWorkloadReady, r.reconcileWorkload, ""},
		{dotvirtv1alpha1.ConditionArgoReady, r.reconcileArgo, ""},
		// It writes the shared ArgoCD instance, outside the render.
		{dotvirtv1alpha1.ConditionSyncImpersonationReady, r.reconcileSyncImpersonation, "sync impersonation"},
		// Before the org webhooks, which need the managed forge's org it ensures;
		// after the rest, so a slow forge only delays phases that call it anyway.
		{dotvirtv1alpha1.ConditionForgeRepoReady, r.reconcilePlatformRepo, "platform-repo bootstrap"},
		{dotvirtv1alpha1.ConditionArgoWebhook, r.reconcileArgoWebhook, "argo webhook"},
		{dotvirtv1alpha1.ConditionDotvirtWebhook, r.reconcileDotvirtWebhook, "dotvirt webhook"},
	} {
		if r.DryRun && phase.dryRunSkip != "" {
			r.dryRunSkip(dv, phase.cond, phase.dryRunSkip)
			continue
		}
		res, err := phase.run(ctx, dv, rc)
		if err != nil {
			r.recordFailure(dv, phase.cond, err)
			return ctrl.Result{}, err
		}
		if res != nil {
			return *res, nil
		}
	}

	dv.Status.ObservedGeneration = dv.Generation
	if r.DryRun {
		logf.FromContext(ctx).Info("dry-run complete: all rendered resources accepted by the API server (nothing persisted)")
		r.setCondition(dv, dotvirtv1alpha1.ConditionAvailable, metav1.ConditionUnknown, "DryRun",
			"dry-run validated the render; nothing was persisted, so nothing serves")
		return ctrl.Result{}, nil
	}
	if wr := meta.FindStatusCondition(dv.Status.Conditions, dotvirtv1alpha1.ConditionWorkloadReady); wr != nil && wr.Status == metav1.ConditionTrue {
		r.setCondition(dv, dotvirtv1alpha1.ConditionAvailable, metav1.ConditionTrue, "Reconciled", "install reconciled and serving")
		dv.Status.Phase = dotvirtv1alpha1.PhaseReady
	} else {
		r.markUnavailable(dv, dotvirtv1alpha1.ConditionWorkloadReady, "WorkloadProgressing", "the dotvirt pod is not ready yet")
		dv.Status.Phase = dotvirtv1alpha1.PhaseProvisioning
	}
	// Only a completed pass knows the full render; a halted one would prune
	// whatever its skipped phases own.
	if digest := rendered.digest(); r.pruneDue(key, digest) {
		if err := r.prune(ctx, dv, rendered); err != nil {
			return ctrl.Result{}, fmt.Errorf("prune: %w", err)
		}
		r.markPruned(key, digest)
	}
	// The watches cover the CR, Deployments, Secrets, Routes and project
	// namespaces; the drift timer heals the rest. No watch reports the platform
	// repo or the ApplicationSet and its Applications, so their recovery polls.
	// Argo gets driftResync to recover on its own; past that, a state only a
	// person fixes waits on the drift timer like any other drift.
	wait := min(r.syncWait(dv), driftResync)
	argo := meta.FindStatusCondition(dv.Status.Conditions, dotvirtv1alpha1.ConditionArgoReady)
	argoSettling := argo != nil && argo.Status != metav1.ConditionTrue && r.clock().Sub(argo.LastTransitionTime.Time) < driftResync
	if argoSettling || meta.IsStatusConditionFalse(dv.Status.Conditions, dotvirtv1alpha1.ConditionForgeRepoReady) {
		wait = min(wait, settleRetry)
	}
	if argo != nil && argo.Reason == reasonRefreshing {
		wait = min(wait, refreshRetry)
	}
	return ctrl.Result{RequeueAfter: wait}, nil
}

// syncWait is when a pass waiting on sync impersonation comes back, since
// neither argocd-cm nor RBAC is watched: Pending, within recheck, for the
// argocd-operator to copy the key; held back on something a person fixes, as
// the verdict ages out; Degraded, with the drift timer like any other drift.
func (r *DotvirtReconciler) syncWait(dv *dotvirtv1alpha1.Dotvirt) time.Duration {
	c := meta.FindStatusCondition(dv.Status.Conditions, dotvirtv1alpha1.ConditionSyncImpersonationReady)
	switch {
	case !dv.Spec.ArgoCD.SyncImpersonation.On() || c == nil || c.Status != metav1.ConditionFalse || c.Reason == "Degraded":
		return driftResync
	case c.Reason == "Pending":
		return r.recheck()
	default:
		return r.verdictTTL()
	}
}

// olderInstall returns the Dotvirt CR that precedes dv, if any: by creation time,
// then namespace/name, so every reconciler picks the same winner. A CR already
// being deleted no longer counts, which is what unblocks its successor.
func (r *DotvirtReconciler) olderInstall(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*dotvirtv1alpha1.Dotvirt, bool, error) {
	var list dotvirtv1alpha1.DotvirtList
	if err := r.List(ctx, &list); err != nil {
		return nil, false, err
	}
	for i := range list.Items {
		o := &list.Items[i]
		if (o.Namespace == dv.Namespace && o.Name == dv.Name) || !o.DeletionTimestamp.IsZero() {
			continue
		}
		if precedes(o, dv) {
			return o, true, nil
		}
	}
	return nil, false, nil
}

func precedes(a, b *dotvirtv1alpha1.Dotvirt) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
}

// resolveSSO fills openShiftSSO into the EFFECTIVE in-memory spec the later
// phases read. Never persisted: it runs after the finalizer add and everything
// later writes only /status (a regression test pins the stored spec); the forge
// and exposure phases fill their resolved hosts in the same way. Unset follows
// the cluster: on where OpenShift's OAuth server runs and no oidc flow is set.
// Off OpenShift even an explicit true is off: the app's flow needs that server.
func (r *DotvirtReconciler) resolveSSO(dv *dotvirtv1alpha1.Dotvirt, oauthServed bool) {
	a := &dv.Spec.Auth
	on := oauthServed && a.OIDC == nil
	if a.OpenShiftSSO != nil {
		on = *a.OpenShiftSSO
	}
	a.OpenShiftSSO = new(on && r.Platform == platform.OpenShift)
}

// reconcileCtx is what one pass derives once, before the phases, for every
// phase to read instead of re-resolving: the ArgoCD target and external URL (a
// Route GET) and the default ingress CA (a ConfigMap GET).
type reconcileCtx struct {
	argoNS, argoSA string
	// argoURL is the externally reachable ArgoCD base URL, "" when neither the
	// spec nor the OpenShift GitOps Route names one (Argo falls back to its poll).
	// argoURLErr says the Route could not be read: "" is then unknown, not
	// absent, and a consumer fails rather than act on it.
	argoURL    string
	argoURLErr error
	// ingressCA is the router CA, read on OpenShift only; ingressCAErr says why it
	// is empty there. How much that matters is the consumer's call: the trust
	// anchors degrade to the system pool, the managed forge's Argo trust blocks.
	ingressCA    string
	ingressCAErr error
	// sync is what the Argo phase rendered for sync impersonation, read by the
	// phase after it.
	sync syncTier
	// tenantAllowed is install.TenantAllowed for this pass, decided once by
	// tenantKinds (tenantKindsRead) so the dotvirt pod and the AppProjects
	// read the same answer.
	tenantAllowed   []tenancy.GroupKind
	tenantKindsRead bool
}

func (r *DotvirtReconciler) newReconcileCtx(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) *reconcileCtx {
	rc := &reconcileCtx{}
	rc.argoNS, rc.argoSA = r.argoTarget(dv)
	rc.argoURL, rc.argoURLErr = r.argoServerURL(ctx, dv, rc.argoNS)
	if r.Platform == platform.OpenShift {
		rc.ingressCA, rc.ingressCAErr = r.readIngressCA(ctx)
	}
	return rc
}

// reconcileDependencies gates on the hard prerequisite: ArgoCD is a PREREQUISITE
// we never install; if absent, record why and requeue (the admin may install the
// prereq operator). KubeVirt/OVN-K/NMState/CDI are soft - note them and proceed.
func (r *DotvirtReconciler) reconcileDependencies(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, _ *reconcileCtx) (*ctrl.Result, error) {
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
		return r.waitPhase(dv, dotvirtv1alpha1.ConditionDependenciesReady, "ProbeFailed",
			err.Error(), dotvirtv1alpha1.PhaseBlockedOnDependencies, time.Minute), nil
	}
	if len(depRes.MissingHard) > 0 {
		dv.Status.ObservedGeneration = dv.Generation
		return r.waitPhase(dv, dotvirtv1alpha1.ConditionDependenciesReady, "MissingPrerequisite",
			depRes.Summary(), dotvirtv1alpha1.PhaseBlockedOnDependencies, time.Minute), nil
	}
	r.resolveSSO(dv, depRes.OpenShiftOAuth)
	r.setCondition(dv, dotvirtv1alpha1.ConditionDependenciesReady, metav1.ConditionTrue, "Satisfied", depRes.Summary())
	return nil, nil
}

// apply server-side-applies obj honoring -dry-run. Every write of a rendered
// object goes through here (or applyOwned) so no call site can pass a literal
// that diverges from r.DryRun, and no object needs the update verb; the
// create-once secrets only gate the apply behind an existence check.
func (r *DotvirtReconciler) apply(ctx context.Context, obj client.Object) error {
	r.recordRender(ctx, obj)
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

func (r *DotvirtReconciler) secret(ctx context.Context, ns, name string) (*corev1.Secret, error) {
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// dryRunSkip records the standard condition for work that mutates state (the
// forge, argocd-secret) server-side dry-run cannot model.
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
	c := forge.NewFactory(base, forge.StaticToken(token), dv.Spec.Forge.InsecureTLS, "").For(dv.Spec.Forge.PlatformRepo)
	if c == nil {
		return nil, fmt.Errorf("cannot parse platform repo URL %q", dv.Spec.Forge.PlatformRepo)
	}
	return c, nil
}

// routeHost reads the spec.host an OpenShift Route carries: the explicit host, or the
// one the router assigned to a hostless Route. "" only when the Route is absent or its
// host isn't assigned yet: callers render the host into pod templates, where a blank
// from a read blip would roll the pods. Unstructured so the module needs no
// openshift/api dep.
func (r *DotvirtReconciler) routeHost(ctx context.Context, ns, name string) (string, error) {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(install.RouteGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, route); err != nil {
		return "", client.IgnoreNotFound(err)
	}
	host, _, _ := unstructured.NestedString(route.Object, "spec", "host")
	return host, nil
}

// argoServerURL resolves the externally reachable ArgoCD base URL: the spec
// override, else the OpenShift GitOps server Route, else "" (the webhook is
// skipped and Argo falls back to its poll). Routes exist only where the route
// API does, so the lookup is gated like the Route watch in SetupWithManager.
func (r *DotvirtReconciler) argoServerURL(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) (string, error) {
	if dv.Spec.ArgoCD.ServerURL != "" {
		return dv.Spec.ArgoCD.ServerURL, nil
	}
	if r.Platform != platform.OpenShift {
		return "", nil
	}
	host, err := r.routeHost(ctx, argoNS, "openshift-gitops-server")
	if err != nil || host == "" {
		return "", err
	}
	return "https://" + host, nil
}

const openShiftGitOpsNS = "openshift-gitops"

// argoTarget resolves the ArgoCD namespace + controller ServiceAccount from the
// spec, defaulting to the platform's conventional install (OpenShift GitOps vs
// community Argo CD).
func (r *DotvirtReconciler) argoTarget(dv *dotvirtv1alpha1.Dotvirt) (ns, sa string) {
	ns, sa = dv.Spec.ArgoCD.Namespace, dv.Spec.ArgoCD.ControllerServiceAccount
	if ns == "" {
		if r.Platform == platform.OpenShift {
			ns = openShiftGitOpsNS
		} else {
			ns = "argocd"
		}
	}
	if sa == "" {
		// By the namespace, not the platform: MicroShift reads as OpenShift and
		// runs community Argo CD.
		if ns == openShiftGitOpsNS {
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

// markUnavailable rolls a blocking phase condition up into Available=False, so
// tooling that watches only Available never reads a stuck install as serving.
func (r *DotvirtReconciler) markUnavailable(dv *dotvirtv1alpha1.Dotvirt, condType, reason, msg string) {
	if condType == dotvirtv1alpha1.ConditionAvailable {
		return
	}
	r.setCondition(dv, dotvirtv1alpha1.ConditionAvailable, metav1.ConditionFalse, reason, condType+": "+msg)
}

// phaseFailure is an error a phase tagged with the condition reason it chose.
type phaseFailure struct {
	reason string
	err    error
}

func (f phaseFailure) Error() string { return f.err.Error() }
func (f phaseFailure) Unwrap() error { return f.err }

// applyFailed tags an operand apply error so the condition shows "ApplyFailed"
// instead of the "Error" a raw error returned from a phase is recorded under.
func applyFailed(err error) error { return phaseFailure{reason: "ApplyFailed", err: err} }

// recordFailure records the phase's failure condition and the Provisioning phase.
func (r *DotvirtReconciler) recordFailure(dv *dotvirtv1alpha1.Dotvirt, condType string, err error) {
	reason := "Error"
	var f phaseFailure
	if errors.As(err, &f) {
		reason = f.reason
	}
	r.setCondition(dv, condType, metav1.ConditionFalse, reason, err.Error())
	r.markUnavailable(dv, condType, reason, err.Error())
	dv.Status.Phase = dotvirtv1alpha1.PhaseProvisioning
}

// waitPhase is the explicit form of an EXPECTED wait: record the not-ready
// condition + phase and hand back the halt result.
// requeue 0 halts without a retry timer (the wait clears via a watch event).
func (r *DotvirtReconciler) waitPhase(dv *dotvirtv1alpha1.Dotvirt, condType, reason, msg, phase string, requeue time.Duration) *ctrl.Result {
	r.setCondition(dv, condType, metav1.ConditionFalse, reason, msg)
	r.markUnavailable(dv, condType, reason, msg)
	dv.Status.Phase = phase
	return &ctrl.Result{RequeueAfter: requeue}
}

// CacheByObject scopes the manager's cache: the watched kinds only where they
// carry the instance label, so the operator never holds every Secret in the
// cluster in memory, and namespaces only where they carry the project label,
// the ones tenant AppProjects derive from. A namespace that loses the label
// leaves the cache, which reads as its deletion.
func CacheByObject(plat platform.Platform) map[client.Object]cache.ByObject {
	hasInstance, _ := labels.NewRequirement(install.InstanceLabel, selection.Exists, nil)
	instanceOnly := cache.ByObject{Label: labels.NewSelector().Add(*hasInstance)}
	hasProject, _ := labels.NewRequirement(install.ProjectLabel, selection.Exists, nil)
	byObject := map[client.Object]cache.ByObject{
		&corev1.Secret{}:     instanceOnly,
		&appsv1.Deployment{}: instanceOnly,
		&corev1.Namespace{}:  {Label: labels.NewSelector().Add(*hasProject)},
		// The watch on Argo's settings selects argocd-cm by name, the only
		// ConfigMap the RBAC lets the operator watch; reads stay direct.
		&corev1.ConfigMap{}: {Field: fields.OneTermEqualSelector("metadata.name", argoSettings)},
	}
	// Registering the Route kind on vanilla Kubernetes fails the cache's REST lookup.
	if plat == platform.OpenShift {
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(install.RouteGVK)
		byObject[route] = instanceOnly
	}
	return byObject
}

// SetupWithManager registers the reconciler; r.Platform must already be set
// (main.go detects it, failing startup rather than guessing).
func (r *DotvirtReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Watch only the owned kinds the RBAC already lets the cache list: a deleted or
	// drifted Deployment/Secret re-reconciles promptly. Services, SAs, PVCs and
	// Ingresses stay unwatched by design (least-privilege RBAC); their drift heals
	// on the driftResync requeue.
	// The CR triggers on spec changes and deletion (which bumps the generation of
	// an object holding finalizers), never on the pass's own status write: an
	// error message carrying an ephemeral port would otherwise re-run the pass
	// back to back against a struggling forge.
	b := ctrl.NewControllerManagedBy(mgr).
		For(&dotvirtv1alpha1.Dotvirt{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("dotvirt").
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Secret{}).
		// A project's namespaces and repo are its AppProject's destinations and
		// source, so a project namespace appearing, leaving or changing either
		// re-derives them.
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.installs),
			builder.WithPredicates(predicate.Funcs{UpdateFunc: tenancyChanged})).
		// Sync impersonation turned on or off in argocd-cm, by dotvirt or by
		// hand, decides what tenant repos may apply (tenantKindsAllowed): the
		// pass re-renders their AppProjects at once, not on the next drift
		// requeue.
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.installs),
			builder.WithPredicates(predicate.Funcs{UpdateFunc: impersonationChanged, GenericFunc: func(event.GenericEvent) bool { return false }}))
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

// installs enqueues every install: a project namespace is an input to each.
func (r *DotvirtReconciler) installs(ctx context.Context, _ client.Object) []reconcile.Request {
	var list dotvirtv1alpha1.DotvirtList
	if err := r.List(ctx, &list); err != nil {
		logf.FromContext(ctx).Error(err, "list installs for a project namespace event")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}

// argoSettings is Argo CD's settings ConfigMap, where sync impersonation is
// turned on.
const argoSettings = "argocd-cm"

// impersonationChanged passes an argocd-cm update that turns sync
// impersonation on or off; the rest of its churn says nothing dotvirt renders.
func impersonationChanged(e event.UpdateEvent) bool {
	setting := func(o client.Object) string {
		cm, _ := o.(*corev1.ConfigMap)
		if cm == nil {
			return ""
		}
		return cm.Data[install.SyncImpersonationKey]
	}
	return setting(e.ObjectOld) != setting(e.ObjectNew)
}

// tenancyChanged passes a namespace update that moves its project or repo; the
// rest of a namespace's churn says nothing about tenancy.
func tenancyChanged(e event.UpdateEvent) bool {
	o, n := e.ObjectOld, e.ObjectNew
	return o.GetLabels()[install.ProjectLabel] != n.GetLabels()[install.ProjectLabel] ||
		o.GetAnnotations()[install.RepoAnnotation] != n.GetAnnotations()[install.RepoAnnotation]
}

// writeStatus persists the pass's status as a merge patch diffed against base,
// the pass's own read. OLM and the status informer touch the object between that
// read and this write often enough that a resourceVersion'd write sprays "object
// has been modified" requeue noise into the log, which reads as a broken install.
// The diff never carries resourceVersion while base and dv share one, and a
// derived field the pass cleared goes out as a null, so it still converges.
func (r *DotvirtReconciler) writeStatus(ctx context.Context, dv, base *dotvirtv1alpha1.Dotvirt) error {
	return r.Status().Patch(ctx, dv, client.MergeFrom(base))
}
