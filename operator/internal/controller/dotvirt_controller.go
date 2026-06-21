package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// Reconcile drives the install in order, recording a status condition per step so a
// stuck install is legible from `kubectl get dotvirt` / `describe`.
func (r *DotvirtReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var dv dotvirtv1alpha1.Dotvirt
	if err := r.Get(ctx, req.NamespacedName, &dv); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("reconciling dotvirt install", "platform", r.Platform)

	// Being deleted: clean up the label-tracked cluster/ArgoCD-namespace resources
	// (the ones not owner-referenceable), then drop the finalizer.
	if !dv.DeletionTimestamp.IsZero() {
		if !r.DryRun && controllerutil.ContainsFinalizer(&dv, dotvirtFinalizer) {
			if err := r.cleanupClusterResources(ctx, &dv); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&dv, dotvirtFinalizer)
			if err := r.Update(ctx, &dv); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	// Ensure the finalizer is present before provisioning anything cluster-scoped.
	// Skipped under -dry-run so a validation run mutates nothing (and the CR stays
	// freely deletable, since the finalizer would otherwise gate its removal).
	if !r.DryRun && controllerutil.AddFinalizer(&dv, dotvirtFinalizer) {
		if err := r.Update(ctx, &dv); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Dependencies: ArgoCD + KubeVirt are hard PREREQUISITES we never install; if
	// either is absent, record why and requeue (the admin may install the prereq
	// operator). OVN-K/NMState/CDI are soft — note them and proceed.
	depRes, err := deps.Probe(r.Config)
	if err != nil {
		log.Error(err, "dependency probe failed")
	}
	if len(depRes.MissingHard) > 0 {
		r.setCondition(&dv, dotvirtv1alpha1.ConditionDependenciesReady, metav1.ConditionFalse, "MissingPrerequisite", depRes.Summary())
		dv.Status.Phase = "BlockedOnDependencies"
		dv.Status.ObservedGeneration = dv.Generation
		if uerr := r.Status().Update(ctx, &dv); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	r.setCondition(&dv, dotvirtv1alpha1.ConditionDependenciesReady, metav1.ConditionTrue, "Satisfied", depRes.Summary())

	// Managed Forgejo (opt-in, eval-grade): stand up + bootstrap a self-hosted forge
	// before anything that needs the forge credential. Once dotvirt-forge exists, the
	// rest of the install can't tell it from a BYO forge.
	if dv.Spec.Forge.Managed {
		if err := r.applyForgejo(ctx, &dv); err != nil {
			return r.failPhase(ctx, &dv, dotvirtv1alpha1.ConditionForgeReady, "ApplyFailed", err)
		}
		switch {
		case r.DryRun:
			r.setCondition(&dv, dotvirtv1alpha1.ConditionForgeReady, metav1.ConditionUnknown, "DryRun", "skipped Forgejo bootstrap in dry-run")
		default:
			ready, err := r.bootstrapForgejo(ctx, &dv)
			if err != nil {
				return r.failPhase(ctx, &dv, dotvirtv1alpha1.ConditionForgeReady, "Error", err)
			}
			if !ready {
				r.setCondition(&dv, dotvirtv1alpha1.ConditionForgeReady, metav1.ConditionFalse, "Progressing", "waiting for Forgejo to come up")
				dv.Status.Phase = "Provisioning"
				if uerr := r.Status().Update(ctx, &dv); uerr != nil {
					return ctrl.Result{}, uerr
				}
				return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
			}
			r.setCondition(&dv, dotvirtv1alpha1.ConditionForgeReady, metav1.ConditionTrue, "Ready", "managed Forgejo bootstrapped")
		}
	}

	// Generated secrets (create-once — never regenerated on re-reconcile, so the
	// cookie key + plugin token survive restarts): the session key and the
	// ApplicationSet plugin token. The forge credential is supplied by the admin
	// (spec.forge.credentialsSecret) or, later, by the managed-Forgejo bootstrap.
	if !r.DryRun {
		for _, s := range []struct{ name, key string }{
			{install.SessionSecretName, "secret"},
			{install.AppsetSecretName, "token"},
			{install.WebhookSecretName, "secret"},
			{install.ArgoWebhookSecretName, "secret"},
		} {
			if err := r.ensureSecret(ctx, &dv, s.name, s.key); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// Namespaced workload: render + server-side-apply, owner-referenced to this CR
	// for automatic GC (unlike the cluster-scoped resources below, which a
	// namespaced CR can't own — those rely on the finalizer).
	nsObjs := []client.Object{
		install.ServiceAccount(&dv),
		install.DraftsPVC(&dv),
		install.Service(&dv),
		install.Deployment(&dv),
	}
	if exposure := r.exposure(&dv); exposure != nil {
		nsObjs = append(nsObjs, exposure)
	}
	for _, obj := range nsObjs {
		if err := controllerutil.SetControllerReference(&dv, obj, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := install.Apply(ctx, r.Client, obj, r.DryRun); err != nil {
			return r.failPhase(ctx, &dv, dotvirtv1alpha1.ConditionAvailable, "ApplyFailed", err)
		}
	}

	// Cluster-scoped + ArgoCD-namespace resources: the RBAC BINDINGS wiring the static
	// operand ClusterRoles (config/rbac/operand_roles.yaml, OLM/kustomize-owned) to the
	// dotvirt SA / Argo controller / platform-admins, plus the AppProject tier (+ the
	// static platform Application). Not owner-referenceable by a namespaced CR, so they
	// carry managed-by labels and the finalizer cleans them up.
	argoNS, argoSA := r.argoTarget(&dv)
	platformRepo := dv.Spec.Forge.PlatformRepo
	// The plugin generator reads the appset token from the ArgoCD namespace, so
	// mirror the generated one there (create-once).
	if !r.DryRun {
		if err := r.mirrorAppsetToken(ctx, &dv, argoNS); err != nil {
			return ctrl.Result{}, err
		}
	}
	clusterObjs := []client.Object{
		install.DotvirtClusterRoleBinding(&dv),
		install.ArgocdApplyClusterRoleBinding(&dv, argoNS, argoSA),
		install.PlatformNetworkAdminBinding(&dv),
		install.TenantsAppProject(&dv, argoNS, platformRepo, dv.Namespace),
		install.AppsetPluginConfigMap(&dv, argoNS, dv.Namespace),
		install.ApplicationSet(&dv, argoNS),
	}
	// The platform tier (its AppProject + the static Application) only applies when a
	// platform repo is configured.
	if platformRepo != "" {
		clusterObjs = append(clusterObjs,
			install.PlatformAppProject(&dv, argoNS, platformRepo),
			install.PlatformApplication(&dv, argoNS, platformRepo),
		)
	}
	// Argo repo-creds (from the forge credential) so Argo can clone the tenant +
	// platform repos, including private ones. Best-effort — nil if the forge URL or
	// secret is absent.
	if rc := r.repoCreds(ctx, &dv, argoNS); rc != nil {
		clusterObjs = append(clusterObjs, rc)
	}
	for _, obj := range clusterObjs {
		if err := install.Apply(ctx, r.Client, obj, r.DryRun); err != nil {
			return r.failPhase(ctx, &dv, dotvirtv1alpha1.ConditionAvailable, "ApplyFailed", err)
		}
	}

	// Platform repo: ensure it exists — the imperative bootstrap pure declarative
	// installers can't do. Skipped in dry-run (a real forge mutation server-side
	// dry-run can't model).
	switch {
	case dv.Spec.Forge.PlatformRepo == "":
		// No platform tier configured; nothing to bootstrap.
	case r.DryRun:
		r.setCondition(&dv, dotvirtv1alpha1.ConditionForgeRepoReady, metav1.ConditionUnknown, "DryRun", "skipped platform-repo bootstrap in dry-run")
	default:
		if err := r.ensurePlatformRepo(ctx, &dv); err != nil {
			r.setCondition(&dv, dotvirtv1alpha1.ConditionForgeRepoReady, metav1.ConditionFalse, "Error", err.Error())
		} else {
			r.setCondition(&dv, dotvirtv1alpha1.ConditionForgeRepoReady, metav1.ConditionTrue, "Ready", "platform repo present")
		}
	}

	// forge→ArgoCD instant sync: one ORG-level webhook covers every repo (present +
	// future) with no per-repo registration. Skipped in dry-run (it mutates the
	// forge + argocd-secret, which server-side dry-run can't model).
	switch {
	case r.DryRun:
		r.setCondition(&dv, dotvirtv1alpha1.ConditionArgoWebhook, metav1.ConditionUnknown, "DryRun", "skipped argo webhook in dry-run")
	default:
		if configured, err := r.ensureArgoWebhook(ctx, &dv, argoNS); err != nil {
			r.setCondition(&dv, dotvirtv1alpha1.ConditionArgoWebhook, metav1.ConditionFalse, "Error", err.Error())
		} else if configured {
			r.setCondition(&dv, dotvirtv1alpha1.ConditionArgoWebhook, metav1.ConditionTrue, "Registered", "org webhook → ArgoCD")
		} else {
			r.setCondition(&dv, dotvirtv1alpha1.ConditionArgoWebhook, metav1.ConditionUnknown, "NotRegistered", "ArgoCD webhook not registered (no Argo URL, or registration deferred); Argo falls back to its poll")
		}
	}

	if r.DryRun {
		log.Info("dry-run complete: all rendered resources accepted by the API server (nothing persisted)")
	}
	r.setCondition(&dv, dotvirtv1alpha1.ConditionAvailable, metav1.ConditionTrue, "Reconciled", "install reconciled")
	dv.Status.Phase = "Ready"
	dv.Status.ObservedGeneration = dv.Generation
	if err := r.Status().Update(ctx, &dv); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// ensurePlatformRepo creates the platform repo on the forge if absent — the
// install-time step a Helm/Kustomize/ArgoCD-app installer structurally can't do
// (it's a forge API call, not a kubectl apply). Reuses the app's shared forge
// client (pkg/forge), driven by the forge credential in the CR's secret.
func (r *DotvirtReconciler) ensurePlatformRepo(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) error {
	secretName := install.ForgeSecretName(dv)
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: secretName}, &s); err != nil {
		return fmt.Errorf("read forge credentials %q: %w", secretName, err)
	}
	token := string(s.Data["token"])
	if dv.Spec.Forge.URL == "" || token == "" {
		return fmt.Errorf("forge url (spec.forge.url) and a credential token (%s/token) are required to bootstrap the platform repo", secretName)
	}
	client := forge.NewFactory(dv.Spec.Forge.URL, token, dv.Spec.Forge.InsecureTLS).For(dv.Spec.Forge.PlatformRepo)
	if client == nil {
		return fmt.Errorf("cannot parse platform repo URL %q", dv.Spec.Forge.PlatformRepo)
	}
	created, err := client.EnsureRepo()
	if err != nil {
		return err
	}
	if created {
		logf.FromContext(ctx).Info("created platform repo", "repo", dv.Spec.Forge.PlatformRepo)
	}
	return nil
}

// resolveExposureType picks the exposure kind for the configured/detected ingress type:
// the explicit spec value, or Route on OpenShift / Ingress on vanilla when "auto"/unset.
func (r *DotvirtReconciler) resolveExposureType(dv *dotvirtv1alpha1.Dotvirt) string {
	if t := string(dv.Spec.Ingress.Type); t != "" && t != "auto" {
		return t
	}
	if r.Platform == platform.OpenShift {
		return "route"
	}
	return "ingress"
}

// exposure builds the UI ingress object for the resolved type — a Route on OpenShift, an
// Ingress on vanilla Kubernetes (nil if Ingress is selected without a host, or for the
// not-yet-implemented Gateway type).
func (r *DotvirtReconciler) exposure(dv *dotvirtv1alpha1.Dotvirt) client.Object {
	switch r.resolveExposureType(dv) {
	case "route":
		return install.Route(dv, dv.Spec.Ingress.Host)
	case "ingress":
		if dv.Spec.Ingress.Host != "" {
			return install.Ingress(dv, dv.Spec.Ingress.Host)
		}
	}
	return nil
}

// forgejoExposure exposes the managed Forgejo on the host derived from
// spec.forge.url (Route on OpenShift, Ingress on vanilla) so its UI + PRs are
// reviewable off-cluster. nil when no external forge URL is set (internal-only).
func (r *DotvirtReconciler) forgejoExposure(dv *dotvirtv1alpha1.Dotvirt) client.Object {
	host := install.ForgejoHost(dv)
	if host == "" {
		return nil
	}
	switch r.resolveExposureType(dv) {
	case "route":
		return install.ForgejoRoute(dv, host)
	case "ingress":
		return install.ForgejoIngress(dv, host)
	}
	return nil
}

// ensureSecret creates a labeled, owner-referenced Secret with a random value if it
// doesn't already exist. Create-once: an existing secret is never regenerated, so
// the session key / plugin token survive re-reconciles and restarts.
func (r *DotvirtReconciler) ensureSecret(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, name, key string) error {
	var existing corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: name}, &existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	value, err := randomHex(32)
	if err != nil {
		return err
	}
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: dv.Namespace, Labels: install.Labels(dv.Name)},
		Data:       map[string][]byte{key: []byte(value)},
	}
	if err := controllerutil.SetControllerReference(dv, s, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, s)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// mirrorAppsetToken copies the generated appset-plugin token into the ArgoCD
// namespace (create-once), where Argo's plugin generator resolves it via the
// ConfigMap's $dotvirt-appset-plugin:token reference.
func (r *DotvirtReconciler) mirrorAppsetToken(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) error {
	var src corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: install.AppsetSecretName}, &src); err != nil {
		return err // the source is ensured earlier in this reconcile
	}
	var existing corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: argoNS, Name: install.AppsetSecretName}, &existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	mirror := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: install.AppsetSecretName, Namespace: argoNS, Labels: install.Labels(dv.Name)},
		Data:       map[string][]byte{"token": src.Data["token"]},
	}
	return r.Create(ctx, mirror)
}

// ensureArgoWebhook registers one ORG-level forge webhook → ArgoCD and sets the
// matching webhook secret in argocd-secret, so a merge triggers an immediate sync
// instead of waiting for Argo's poll. Best-effort: returns configured=false (no error)
// when no Argo URL is resolvable, no platform repo names the org, or the forge
// registration itself transiently fails (logged) — Argo's poll backstops a missed nudge,
// so none of those should fail the install. err is reserved for operator-internal
// failures (reading the webhook secret / forge credentials, applying argocd-secret).
// Real-only (the caller skips it in dry-run) — it mutates the forge + argocd-secret.
func (r *DotvirtReconciler) ensureArgoWebhook(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) (configured bool, err error) {
	if dv.Spec.Forge.URL == "" || dv.Spec.Forge.PlatformRepo == "" {
		return false, nil
	}
	argoURL := r.argoServerURL(ctx, dv, argoNS)
	if argoURL == "" {
		return false, nil
	}
	// The shared webhook secret, generated create-once in the dotvirt namespace.
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: install.ArgoWebhookSecretName}, &s); err != nil {
		return false, err
	}
	value := string(s.Data["secret"])
	// Argo verifies the delivery signature against this key (own only the key).
	if err := install.Apply(ctx, r.Client, install.ArgoWebhookSecret(argoNS, value), false); err != nil {
		return false, fmt.Errorf("set argo webhook secret: %w", err)
	}
	// One org webhook covers every repo, using the forge credential to register it.
	name := install.ForgeSecretName(dv)
	var fs corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: name}, &fs); err != nil {
		return false, fmt.Errorf("read forge credentials %q: %w", name, err)
	}
	client := forge.NewFactory(dv.Spec.Forge.URL, string(fs.Data["token"]), dv.Spec.Forge.InsecureTLS).For(dv.Spec.Forge.PlatformRepo)
	if client == nil {
		return false, fmt.Errorf("cannot parse platform repo URL %q", dv.Spec.Forge.PlatformRepo)
	}
	// Registering the hook is best-effort, like the app's own webhook sweep
	// (cmd/dotvirt logs and moves on): a transient forge hiccup here must not read as a
	// hard install error, because ArgoCD's poll already backstops a missed nudge. Log it
	// and report unconfigured so the next reconcile retries. The secret/credential steps
	// above stay hard errors — those are operator-internal, not forge-transient.
	if err := client.EnsureOrgWebhook(strings.TrimRight(argoURL, "/")+"/api/webhook", value); err != nil {
		logf.FromContext(ctx).Info("argo webhook registration deferred; ArgoCD poll backstops", "error", err.Error())
		return false, nil
	}
	return true, nil
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

// applyForgejo renders the managed Forgejo workload (SA, Deployment with the verified
// bootstrap initContainer, Service) and the data PVC. The rootless image runs under
// dotvirt's standard non-root securityContext, so no SCC binding is needed. Everything
// but the PVC is owner-referenced for auto-cleanup; the PVC is orphaned so the git
// data survives uninstall.
func (r *DotvirtReconciler) applyForgejo(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) error {
	if !r.DryRun {
		if err := r.ensureSecret(ctx, dv, install.ForgejoAdminSecret, "password"); err != nil {
			return err
		}
	}
	// PVC first (orphan: no ownerRef, so the git data survives uninstall) — applied
	// before the Deployment that mounts it, so a retry of any later resource can't
	// strand the pod Pending on a missing volume.
	if err := install.Apply(ctx, r.Client, install.ForgejoPVC(dv), r.DryRun); err != nil {
		return err
	}
	owned := []client.Object{
		install.ForgejoServiceAccount(dv),
		install.ForgejoService(dv),
		// fsGroup only on vanilla K8s; OpenShift's restricted-v2 injects its own.
		install.ForgejoDeployment(dv, r.Platform != platform.OpenShift),
	}
	if exp := r.forgejoExposure(dv); exp != nil {
		owned = append(owned, exp)
	}
	for _, o := range owned {
		if err := controllerutil.SetControllerReference(dv, o, r.Scheme); err != nil {
			return err
		}
		if err := install.Apply(ctx, r.Client, o, r.DryRun); err != nil {
			return err
		}
	}
	return nil
}

// bootstrapForgejo mints the scoped token + ensures the owner org once the managed
// Forgejo is up, then writes the dotvirt-forge secret. Idempotent: a no-op once
// dotvirt-forge exists; returns ready=false (caller requeues) while Forgejo isn't up.
func (r *DotvirtReconciler) bootstrapForgejo(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (bool, error) {
	credName := install.ForgeSecretName(dv)
	// Already bootstrapped AND the stored token still works? Trusting mere existence
	// leaves a dead token in place forever after a Forgejo data reset or out-of-band
	// rotation (Argo + the app then fail auth). Validate it; only short-circuit when
	// it's genuinely valid, else fall through to re-mint. A forge blip surfaces as
	// err (requeue) rather than a needless re-mint.
	var existing corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: credName}, &existing); err == nil {
		valid, err := forge.NewFactory(dv.Spec.Forge.URL, "unused", dv.Spec.Forge.InsecureTLS).
			ValidateToken(string(existing.Data["token"]))
		if err != nil {
			return false, err
		}
		if valid {
			return true, nil
		}
		logf.FromContext(ctx).Info("stored forge token rejected — re-minting", "secret", credName)
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	// Forgejo up yet?
	var d appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: install.ForgejoServiceName}, &d); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if d.Status.AvailableReplicas < 1 {
		return false, nil
	}
	// Mint the scoped token via basic auth as the bootstrapped admin.
	var admin corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: install.ForgejoAdminSecret}, &admin); err != nil {
		return false, err
	}
	url := dv.Spec.Forge.URL
	token, err := forge.NewFactory(url, "unused", dv.Spec.Forge.InsecureTLS).
		MintToken(install.ForgejoBotUser, string(admin.Data["password"]), "dotvirt-operator", []string{"write:organization", "write:repository"})
	if err != nil {
		return false, err
	}
	if err := r.writeForgeSecret(ctx, dv, credName, url, install.ForgejoBotUser, token); err != nil {
		return false, err
	}
	// Ensure the owner org exists (repos live under it for the org-level webhook).
	if dv.Spec.Forge.PlatformRepo != "" {
		if c := forge.NewFactory(url, token, dv.Spec.Forge.InsecureTLS).For(dv.Spec.Forge.PlatformRepo); c != nil {
			if err := c.EnsureOrg(); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// writeForgeSecret upserts the dotvirt-forge credential from the managed Forgejo's
// minted token, so the rest of the install treats it like a BYO forge. Upsert (not
// create-once) so a re-mint of a rejected token overwrites the stale value in place.
func (r *DotvirtReconciler) writeForgeSecret(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, name, url, username, token string) error {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: dv.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, s, func() error {
		s.Labels = install.Labels(dv.Name)
		s.StringData = map[string]string{"url": url, "username": username, "token": token}
		return controllerutil.SetControllerReference(dv, s, r.Scheme)
	})
	return err
}

// repoCreds builds the Argo repo-credentials Secret from the forge credential, or
// nil when the forge URL/secret is unavailable (best-effort — Argo then relies on
// anonymous read for public repos).
func (r *DotvirtReconciler) repoCreds(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) client.Object {
	if dv.Spec.Forge.URL == "" {
		return nil
	}
	name := install.ForgeSecretName(dv)
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: name}, &s); err != nil {
		return nil
	}
	token := string(s.Data["token"])
	if token == "" {
		return nil
	}
	prefix := dv.Spec.Forge.URL
	if dv.Spec.Forge.PlatformRepo != "" {
		prefix = forge.OwnerPrefixURL(dv.Spec.Forge.PlatformRepo)
	}
	return install.RepoCredsSecret(dv, argoNS, prefix, string(s.Data["username"]), token)
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

// cleanupClusterResources deletes the label-selected cluster-scoped + ArgoCD-
// namespace resources provisioned for this instance (the ones a namespaced CR
// can't ownerRef). Not-found and missing-CRD are tolerated so cleanup is idempotent.
func (r *DotvirtReconciler) cleanupClusterResources(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) error {
	sel := client.MatchingLabels{"dotvirt.io/instance": dv.Name}
	// Only the ClusterRoleBindings are ours to delete — the operand ClusterRoles are
	// static, OLM/kustomize-owned (config/rbac/operand_roles.yaml) and shared.
	if err := r.DeleteAllOf(ctx, &rbacv1.ClusterRoleBinding{}, sel); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	argoNS, _ := r.argoTarget(dv)
	for _, kind := range []string{"AppProject", "Application", "ApplicationSet"} {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(install.ArgoGVK(kind))
		if err := r.DeleteAllOf(ctx, u, client.InNamespace(argoNS), sel); err != nil &&
			!apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return err
		}
	}
	// The ArgoCD-namespace plugin ConfigMap + mirrored token Secret.
	for _, o := range []client.Object{&corev1.ConfigMap{}, &corev1.Secret{}} {
		if err := r.DeleteAllOf(ctx, o, client.InNamespace(argoNS), sel); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
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
func (r *DotvirtReconciler) failPhase(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, condType, reason string, err error) (ctrl.Result, error) {
	r.setCondition(dv, condType, metav1.ConditionFalse, reason, err.Error())
	dv.Status.Phase = "Provisioning"
	if uerr := r.Status().Update(ctx, dv); uerr != nil {
		logf.FromContext(ctx).Error(uerr, "status update failed", "phase", "Provisioning")
	}
	return ctrl.Result{}, err
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
	return ctrl.NewControllerManagedBy(mgr).
		For(&dotvirtv1alpha1.Dotvirt{}).
		Named("dotvirt").
		Complete(r)
}
