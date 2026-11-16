package controller

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/forge"
)

// reconcileArgo applies the cluster-scoped + ArgoCD-namespace resources: the RBAC
// BINDINGS wiring the static operand ClusterRoles (config/rbac/operand_roles.yaml,
// OLM/kustomize-owned) to the dotvirt SA / Argo controller / platform-admins, plus
// the AppProjects (one per tenant project, the platform tier) and the
// ApplicationSet (+ the static platform Application). Not owner-referenceable
// by a namespaced CR, so they carry managed-by labels and the finalizer cleans
// them up.
func (r *DotvirtReconciler) reconcileArgo(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (*ctrl.Result, error) {
	argoNS, argoSA := rc.argoNS, rc.argoSA
	platformRepo := dv.Spec.Forge.PlatformRepo
	// The plugin generator reads the appset token from the ArgoCD namespace, so
	// mirror the generated one there. Dry-run skips both this and the TLS trust
	// merge: the mirror copies a Secret the (skipped) secrets phase never
	// generated, and the trust merge writes a shared, unlabeled ConfigMap outside
	// the render. The rendered objects below still validate against the API server.
	if !r.DryRun {
		if err := r.mirrorAppsetToken(ctx, dv, argoNS); err != nil {
			return nil, err
		}
		if err := r.ensureForgeTLSTrust(ctx, dv, rc); err != nil {
			return nil, err
		}
	}
	platformNS := r.platformNamespaces(dv, argoNS)
	tenants, err := r.tenantAppProjects(ctx, dv, rc, platformNS)
	if err != nil {
		return nil, err
	}
	appset := install.ApplicationSet(dv, argoNS, platformNS, tenants.unconfined)
	syncRequest := install.SyncRequestRoleBinding(dv, argoNS)
	if argoNS == dv.Namespace {
		// Prune never sweeps the install namespace: the ownerRef lets the
		// garbage collector take the binding with the CR.
		if err := controllerutil.SetControllerReference(dv, syncRequest, r.Scheme); err != nil {
			return nil, err
		}
	}
	objs := []client.Object{
		install.DotvirtClusterRoleBinding(dv),
		syncRequest,
		install.ArgocdReadClusterRoleBinding(dv, argoNS, argoSA),
		install.PlatformNetworkAdminBinding(dv),
	}
	if b := install.ArgocdApplyClusterRoleBinding(dv, argoNS, argoSA, platformRepo != "", tenants.controllerApplies); b != nil {
		objs = append(objs, b)
	}
	if !r.ClusterVersioned {
		objs = append(objs, install.StorageCatalogBinding(dv))
	}
	// The tenant AppProjects precede the ApplicationSet whose template names
	// them, and apply stops at the first failure: the template never switches
	// an Application to a project that does not exist yet.
	objs = append(objs, tenants.objs...)
	objs = append(objs, install.AppsetPluginConfigMap(dv, argoNS, dv.Namespace), appset)
	// The platform tier (its AppProject + the static Application) only applies when a
	// platform repo is configured.
	if platformRepo != "" {
		objs = append(objs,
			install.PlatformSyncClusterRoleBinding(dv, argoNS),
			install.PlatformAppProject(dv, argoNS, platformRepo),
			install.PlatformApplication(dv, argoNS, platformRepo),
		)
	}
	// Argo repo-creds (from the forge credential) so Argo can clone the tenant +
	// platform repos, including private ones. Best-effort - nil if the forge URL or
	// secret is absent.
	creds, err := r.repoCreds(ctx, dv, argoNS)
	if err != nil {
		return nil, err
	}
	if creds != nil {
		objs = append(objs, creds)
	}
	rc.sync = syncTier{accounts: tenants.accounts, platform: platformRepo != "", sharedInUse: tenants.sharedInUse, appProjects: map[string]bool{}}
	for _, obj := range objs {
		if err := r.apply(ctx, obj); err != nil {
			return nil, applyFailed(err)
		}
		if obj.GetObjectKind().GroupVersionKind() == install.ArgoGVK("AppProject") {
			rc.sync.appProjects[obj.GetName()] = true
		}
	}
	if r.DryRun {
		r.dryRunSkip(dv, dotvirtv1alpha1.ConditionArgoReady, "appset-token mirror and forge TLS trust")
		return nil, nil
	}
	// The apply answered with the live object, status and annotations included.
	state := judgeAppSet(appset, meta.FindStatusCondition(dv.Status.Conditions, dotvirtv1alpha1.ConditionWorkloadReady), r.clock())
	if state.refresh {
		if err := r.refreshAppSet(ctx, appset); err != nil {
			return nil, fmt.Errorf("ask the ApplicationSet to run again: %w", err)
		}
	}
	r.setArgoReady(dv, tenants, state)
	return nil, nil
}

// reconcileArgoWebhook sets up forge->ArgoCD instant sync: one ORG-level webhook
// covers every repo (present + future) with no per-repo registration. Skipped in
// dry-run (it mutates the forge + argocd-secret, which server-side dry-run can't
// model). A registration failure is recorded on the condition but doesn't halt
// the pipeline - Argo falls back to its poll.
func (r *DotvirtReconciler) reconcileArgoWebhook(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (*ctrl.Result, error) {
	status, reason, msg := r.argoWebhook(ctx, dv, rc)
	r.setCondition(dv, dotvirtv1alpha1.ConditionArgoWebhook, status, reason, msg)
	return nil, nil
}

// mirrorAppsetToken copies the generated appset-plugin token into the ArgoCD
// namespace, where Argo's plugin generator resolves it via the ConfigMap's
// $dotvirt-appset-plugin:token reference.
//
// The source token is create-once, but the MIRROR must converge: a mirror left
// behind by an earlier install (same name, different generated token) makes the
// plugin generator 401 forever. Nothing else fails, so the break is silent: the
// ApplicationSet generates no parameters, no tenant Application is ever created,
// every VM reads NotTracked, and merged manifests are never applied.
func (r *DotvirtReconciler) mirrorAppsetToken(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) error {
	src, err := r.secret(ctx, dv.Namespace, install.AppsetSecretName)
	if err != nil {
		return err // the source is ensured earlier in this reconcile
	}
	// One mirror name per ArgoCD namespace, so a second install sharing that namespace
	// would rewrite this one on every reconcile while the first rewrote it back, leaving
	// both plugin generators intermittently 401ing. Re-stamping the labels would also
	// pull the other install's Secret into this one's uninstall blast radius. Fail
	// loudly instead: the topology needs one ArgoCD namespace per install.
	existing, err := r.secret(ctx, argoNS, install.AppsetSecretName)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if owner := existing.Labels[install.InstanceLabel]; owner != "" && !install.OwnedBy(existing.Labels, dv) {
			return fmt.Errorf("appset token mirror %s/%s belongs to dotvirt install %s/%s; give each install its own ArgoCD namespace",
				argoNS, install.AppsetSecretName, existing.Labels[install.InstanceNamespaceLabel], owner)
		}
	}
	// The labels adopt an unlabeled predecessor's mirror, so it is cleaned up with
	// this instance.
	return r.apply(ctx, &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: install.AppsetSecretName, Namespace: argoNS, Labels: install.Labels(dv)},
		Data:       map[string][]byte{"token": src.Data["token"]},
	})
}

// argoWebhook registers one ORG-level forge webhook -> ArgoCD and sets the
// matching webhook secret in argocd-secret, so a merge triggers an immediate sync
// instead of waiting for Argo's poll. Best-effort: Unknown (no error) when no Argo
// URL is resolvable, no platform repo names the org, the ArgoCD resource manages
// the webhook keys itself, or the forge registration transiently fails (logged) -
// Argo's poll backstops a missed nudge, so none of those should fail the install.
// False is reserved for operator-side failures (reading the ArgoCD Route or
// resource, the webhook secret or the forge credentials, setting argocd-secret's key).
// Real-only (the caller skips it in dry-run) - it mutates the forge + argocd-secret.
func (r *DotvirtReconciler) argoWebhook(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) (metav1.ConditionStatus, string, string) {
	failed := func(err error) (metav1.ConditionStatus, string, string) {
		return metav1.ConditionFalse, "Error", err.Error()
	}
	notRegistered := func() (metav1.ConditionStatus, string, string) {
		return metav1.ConditionUnknown, "NotRegistered", "ArgoCD webhook not registered (no Argo URL, or registration deferred); Argo falls back to its poll"
	}
	if dv.Spec.Forge.URL == "" || dv.Spec.Forge.PlatformRepo == "" {
		return notRegistered()
	}
	if rc.argoURLErr != nil {
		return failed(fmt.Errorf("read the ArgoCD Route: %w", rc.argoURLErr))
	}
	if rc.argoURL == "" {
		return notRegistered()
	}
	hookURL := strings.TrimRight(rc.argoURL, "/") + "/api/webhook"
	// With spec.webhookSecrets set, the argocd-operator writes the webhook keys
	// declared there and deletes the rest, so a key set here would verify
	// nothing; the hook is left to whoever declares the secret there.
	instance := rc.argoNS + "/" + argoInstance(rc.argoNS)
	inst, _, err := r.argoInstanceState(ctx, instance)
	if err != nil {
		return failed(err)
	}
	if install.DeclaresWebhookSecrets(inst) {
		return metav1.ConditionUnknown, "ManagedByArgoCD", fmt.Sprintf(
			"ArgoCD %s sets spec.webhookSecrets, so it owns argocd-secret's webhook keys: declare spec.webhookSecrets.gogs, then give the Forgejo org webhook to %s (push events) that secret, replacing the one dotvirt registered there, if any, rather than adding a second; Argo falls back to its poll meanwhile",
			instance, hookURL)
	}
	// The shared webhook secret, generated create-once in the dotvirt namespace.
	s, err := r.secret(ctx, dv.Namespace, install.ArgoWebhookSecretName)
	if err != nil {
		return failed(err)
	}
	value := string(s.Data["secret"])
	if err := r.applyArgoWebhookKey(ctx, rc.argoNS, value, hookURL); err != nil {
		return failed(err)
	}
	// One org webhook covers every repo, using the forge credential to register it.
	fc, err := r.forgeClient(ctx, dv)
	if err != nil {
		return failed(err)
	}
	// Registering the hook is best-effort, like the app's own webhook sweep
	// (cmd/dotvirt logs and moves on): a transient forge hiccup here must not read as a
	// hard install error, because ArgoCD's poll already backstops a missed nudge. Log it
	// and report unconfigured so the next reconcile retries. The secret/credential steps
	// above stay hard errors - those are operator-internal, not forge-transient.
	if err := fc.EnsureOrgPushWebhook(hookURL, value); err != nil {
		logf.FromContext(ctx).Info("argo webhook registration deferred; ArgoCD poll backstops", "error", err.Error())
		return notRegistered()
	}
	return metav1.ConditionTrue, "Registered", "org webhook -> ArgoCD"
}

// applyArgoWebhookKey sets the operator's key in argoNS's argocd-secret, then
// drops the legacy key. Only the apply's conflict means another manager holds
// the key: the legacy patch's is a resourceVersion race.
func (r *DotvirtReconciler) applyArgoWebhookKey(ctx context.Context, argoNS, value, hookURL string) error {
	s := install.ArgoWebhookSecret(argoNS, value)
	err := r.applyShared(ctx, s)
	if apierrors.IsConflict(err) {
		return fmt.Errorf("%s/%s already sets %s under another manager, which the operator leaves alone: remove that key to hand it to the operator, or register a Forgejo org webhook (push events) to %s with its value: %w",
			argoNS, install.ArgoSecretName, install.ArgoWebhookKey, hookURL, err)
	}
	if err != nil {
		return fmt.Errorf("set argo webhook secret: %w", err)
	}
	return r.dropLegacyArgoWebhookKey(ctx, s, value)
}

// releaseArgoWebhookKey removes from argoNS's argocd-secret the webhook keys
// the operator wrote. A key another manager also set stays; nothing to do when
// the Secret is gone.
func (r *DotvirtReconciler) releaseArgoWebhookKey(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) error {
	s := &corev1.Secret{}
	err := r.reader().Get(ctx, types.NamespacedName{Namespace: argoNS, Name: install.ArgoSecretName}, s)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s/%s: %w", argoNS, install.ArgoSecretName, err)
	}
	if install.HoldsArgoWebhookKey(s) {
		// An apply naming no key drops the ones only this manager held.
		s = install.ArgoWebhookSecret(argoNS, "")
		if err := r.applyShared(ctx, s); err != nil {
			return fmt.Errorf("release %s from %s/%s: %w", install.ArgoWebhookKey, argoNS, install.ArgoSecretName, err)
		}
	}
	own, err := r.secret(ctx, dv.Namespace, install.ArgoWebhookSecretName)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.dropLegacyArgoWebhookKey(ctx, s, string(own.Data["secret"]))
}

// dropLegacyArgoWebhookKey removes install.LegacyArgoWebhookKey from s, the
// live argocd-secret, when it holds value, the operator's own secret. The
// value is the proof of authorship: the operator wrote that key through
// stringData, whose field ownership its next apply discards, and SSA never
// removes a data key on its behalf. Any other value is another manager's and
// stays.
func (r *DotvirtReconciler) dropLegacyArgoWebhookKey(ctx context.Context, s *corev1.Secret, value string) error {
	if value == "" || string(s.Data[install.LegacyArgoWebhookKey]) != value {
		return nil
	}
	base := s.DeepCopy()
	delete(s.Data, install.LegacyArgoWebhookKey)
	if err := r.Patch(ctx, s, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("remove %s from %s/%s: %w", install.LegacyArgoWebhookKey, s.Namespace, install.ArgoSecretName, err)
	}
	return nil
}

// repoCreds builds the Argo repo-credentials Secret from the forge credential, or
// nil when there is no forge URL or credential (Argo then relies on anonymous
// read for public repos). A read error is returned, not read as absent: prune
// would delete the live repo-creds on a transient failure.
func (r *DotvirtReconciler) repoCreds(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) (client.Object, error) {
	if dv.Spec.Forge.URL == "" {
		return nil, nil
	}
	s, err := r.secret(ctx, dv.Namespace, install.ForgeSecretName(dv))
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	token := string(s.Data["token"])
	if token == "" {
		return nil, nil
	}
	prefix := dv.Spec.Forge.URL
	if dv.Spec.Forge.PlatformRepo != "" {
		prefix = forge.OwnerPrefixURL(dv.Spec.Forge.PlatformRepo)
	}
	return install.RepoCredsSecret(dv, argoNS, prefix, string(s.Data["username"]), token), nil
}

// ensureForgeTLSTrust merges the forge host + ingress CA into argocd-tls-certs-cm
// so repo-server VERIFIES the router cert. repo-creds templates silently ignore
// `insecure` (repository-secret-only field); without this entry the install only
// worked after an invisible hand-added cert. Managed-on-OpenShift only. MERGE:
// never touch other hosts' entries. Left on uninstall: not a secret, and a dead
// host key is inert.
func (r *DotvirtReconciler) ensureForgeTLSTrust(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) error {
	if !dv.Spec.Forge.Managed || r.Platform != platform.OpenShift {
		return nil
	}
	// The ingress CA signs what the router serves; a Gateway holds its own
	// certificate, and MicroShift publishes no ingress CA at all.
	if r.resolveExposureType(dv) == dotvirtv1alpha1.IngressGateway {
		return nil
	}
	host := install.ForgejoHost(dv)
	if host == "" {
		return nil
	}
	// A plain-http forge URL (an in-cluster service, an eval install) has no TLS
	// to trust; requiring the ingress CA would wedge the whole reconcile on
	// platforms that don't publish default-ingress-cert (MicroShift).
	if u, err := url.Parse(dv.Spec.Forge.URL); err == nil && u.Scheme == "http" {
		return nil
	}
	if rc.ingressCAErr != nil {
		// Legible and blocking: without trust every Application wedges in a
		// ComparisonError that says x509, three layers away from this cause.
		return fmt.Errorf("trust the managed forge's route in ArgoCD: %w", rc.ingressCAErr)
	}
	// SSA with ONLY our host key: ConfigMap data keys get per-key field
	// ownership, so the merge cannot clobber the gitops operator's writes the way
	// a whole-object read-modify-write would race them. No instance labels: the
	// CM is shared, and labeling it would pull it into finalizer cleanup.
	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-tls-certs-cm", Namespace: rc.argoNS},
		Data:       map[string]string{host: rc.ingressCA},
	}
	return r.apply(ctx, cm)
}
