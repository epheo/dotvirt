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
	"k8s.io/apimachinery/pkg/runtime/schema"
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

// DotvirtReconciler provisions a dotvirt install from a Dotvirt resource: it renders
// the core resources (RBAC, Deployment, Route/Ingress, the AppProject tier + the
// platform Argo app) and bootstraps the platform git repo. dotvirt's RUNTIME still
// owns nothing — this operator is the install-time provisioner (the automated form
// of today's manual `oc apply` + repo creation), so it holds the privileged
// install RBAC and forge-admin credential the app never touches.
type DotvirtReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Config   *rest.Config      // for discovery (dependency probe + platform detect)
	Platform platform.Platform // detected once in SetupWithManager
	DryRun   bool              // -dry-run: validate via server-side dry-run apply; persist nothing
}

// The operator's OWN least-privilege RBAC (generated into config/rbac/role.yaml). Verbs
// are exactly what the controller's client does: install.Apply is server-side-apply, i.e.
// create+patch (never update); only the kinds it actually reads (dotvirts, secrets,
// deployments, routes) get list+watch (the cache); cleanup's DeleteAllOf is the
// `deletecollection` verb. The operator does NOT author ClusterRoles — it only `bind`s the
// three static operand roles — so it needs no `escalate` and no ClusterRole/RoleBinding writes.
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dotvirt.io,resources=dotvirts/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;deletecollection
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=create;patch;deletecollection
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts;persistentvolumeclaims,verbs=create;patch
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;patch
// routes/custom-host: required to set an explicit spec.host on a Route (the forge + app exposure hosts).
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes/custom-host,verbs=create
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=create;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=appprojects;applications;applicationsets,verbs=create;patch;deletecollection
// clusterrolebindings: the operator creates the bindings that wire the static operand roles
// to the dotvirt SA / Argo controller / platform-admins, and DeleteAllOf-cleans them up.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=create;patch;deletecollection
// clusterroles `bind`: the operator's ONLY rbac-authoring right — bind these three named
// static roles into the bindings above. No escalate, no role create/update/delete.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,resourceNames=dotvirt;dotvirt-argocd-apply;dotvirt-platform-network-admin,verbs=bind

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
		r.reconcilePlatformRepo,
		r.reconcileArgoWebhook,
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
	if err := r.Status().Update(ctx, &dv); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileDependencies gates on the hard prerequisites: ArgoCD + KubeVirt are
// PREREQUISITES we never install; if either is absent, record why and requeue (the
// admin may install the prereq operator). OVN-K/NMState/CDI are soft — note them
// and proceed.
func (r *DotvirtReconciler) reconcileDependencies(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	depRes, err := deps.Probe(r.Config)
	if err != nil {
		logf.FromContext(ctx).Error(err, "dependency probe failed")
	}
	if len(depRes.MissingHard) > 0 {
		r.setCondition(dv, dotvirtv1alpha1.ConditionDependenciesReady, metav1.ConditionFalse, "MissingPrerequisite", depRes.Summary())
		dv.Status.Phase = dotvirtv1alpha1.PhaseBlockedOnDependencies
		dv.Status.ObservedGeneration = dv.Generation
		if uerr := r.Status().Update(ctx, dv); uerr != nil {
			return nil, uerr
		}
		return &ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	r.setCondition(dv, dotvirtv1alpha1.ConditionDependenciesReady, metav1.ConditionTrue, "Satisfied", depRes.Summary())
	return nil, nil
}

// forgeClient reads the install's forge credential (ForgeSecretName — the
// admin-supplied BYO secret, or the one the managed-Forgejo bootstrap minted) and
// builds the app's shared forge client (pkg/forge) scoped to the platform repo.
func (r *DotvirtReconciler) forgeClient(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*forge.Client, error) {
	name := install.ForgeSecretName(dv)
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: name}, &s); err != nil {
		return nil, fmt.Errorf("read forge credentials %q: %w", name, err)
	}
	token := string(s.Data["token"])
	if dv.Spec.Forge.URL == "" || token == "" {
		return nil, fmt.Errorf("forge url (spec.forge.url) and a credential token (%s/token) are required", name)
	}
	c := forge.NewFactory(dv.Spec.Forge.URL, token, dv.Spec.Forge.InsecureTLS).For(dv.Spec.Forge.PlatformRepo)
	if c == nil {
		return nil, fmt.Errorf("cannot parse platform repo URL %q", dv.Spec.Forge.PlatformRepo)
	}
	return c, nil
}

// argoServerURL resolves the externally reachable ArgoCD base URL: the spec
// override, else the OpenShift GitOps server Route, else "" (caller falls back to
// Argo's poll).
func (r *DotvirtReconciler) argoServerURL(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) string {
	if dv.Spec.ArgoCD.ServerURL != "" {
		return dv.Spec.ArgoCD.ServerURL
	}
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
	if err := r.Get(ctx, types.NamespacedName{Namespace: argoNS, Name: "openshift-gitops-server"}, route); err != nil {
		return ""
	}
	host, _, _ := unstructured.NestedString(route.Object, "spec", "host")
	if host == "" {
		return ""
	}
	return "https://" + host
}

// argoTarget resolves the ArgoCD namespace + controller ServiceAccount from the
// spec, defaulting per detected platform (openshift-gitops vs argocd).
func (r *DotvirtReconciler) argoTarget(dv *dotvirtv1alpha1.Dotvirt) (ns, sa string) {
	ns, sa = dv.Spec.ArgoCD.Namespace, dv.Spec.ArgoCD.ControllerServiceAccount
	if ns == "" {
		ns = r.Platform.DefaultArgoNamespace()
	}
	if sa == "" {
		sa = r.Platform.DefaultArgoController()
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
	if uerr := r.Status().Update(ctx, dv); uerr != nil {
		logf.FromContext(ctx).Error(uerr, "status update failed", "phase", dotvirtv1alpha1.PhaseProvisioning)
	}
	return err
}

// SetupWithManager detects the platform once and registers the reconciler.
// Detection FAILS startup rather than defaulting: a wrong platform silently
// mis-renders every platform-gated resource (most damagingly fsGroup, which an
// OpenShift SCC then rejects — bricking Forgejo). Failing loud turns a transient
// discovery-API blip at boot into a quick pod restart that retries, instead of a
// permanent mis-render from an empty/guessed r.Platform.
func (r *DotvirtReconciler) SetupWithManager(mgr ctrl.Manager) error {
	plat, err := platform.Detect(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("detect platform: %w", err)
	}
	r.Platform = plat
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
	if plat == platform.OpenShift {
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
		b = b.Owns(route)
	}
	return b.Complete(r)
}
