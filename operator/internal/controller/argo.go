package controller

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
	"github.com/epheo/dotvirt/pkg/forge"
)

// reconcileArgo applies the cluster-scoped + ArgoCD-namespace resources: the RBAC
// BINDINGS wiring the static operand ClusterRoles (config/rbac/operand_roles.yaml,
// OLM/kustomize-owned) to the dotvirt SA / Argo controller / platform-admins, plus
// the AppProject tier (+ the static platform Application). Not owner-referenceable
// by a namespaced CR, so they carry managed-by labels and the finalizer cleans
// them up.
func (r *DotvirtReconciler) reconcileArgo(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	argoNS, argoSA := r.argoTarget(dv)
	platformRepo := dv.Spec.Forge.PlatformRepo
	// The plugin generator reads the appset token from the ArgoCD namespace, so
	// mirror the generated one there (create-once).
	if !r.DryRun {
		if err := r.mirrorAppsetToken(ctx, dv, argoNS); err != nil {
			return nil, err
		}
		if err := r.ensureForgeTLSTrust(ctx, dv, argoNS); err != nil {
			return nil, err
		}
	}
	objs := []client.Object{
		install.DotvirtClusterRoleBinding(dv),
		install.ArgocdApplyClusterRoleBinding(dv, argoNS, argoSA),
		install.PlatformNetworkAdminBinding(dv),
		install.TenantsAppProject(dv, argoNS, platformRepo, dv.Namespace),
		install.AppsetPluginConfigMap(dv, argoNS, dv.Namespace),
		install.ApplicationSet(dv, argoNS),
	}
	// The platform tier (its AppProject + the static Application) only applies when a
	// platform repo is configured.
	if platformRepo != "" {
		objs = append(objs,
			install.PlatformAppProject(dv, argoNS, platformRepo),
			install.PlatformApplication(dv, argoNS, platformRepo),
		)
	}
	// Argo repo-creds (from the forge credential) so Argo can clone the tenant +
	// platform repos, including private ones. Best-effort — nil if the forge URL or
	// secret is absent.
	if rc := r.repoCreds(ctx, dv, argoNS); rc != nil {
		objs = append(objs, rc)
	}
	for _, obj := range objs {
		if err := install.Apply(ctx, r.Client, obj, r.DryRun); err != nil {
			return nil, r.failPhase(ctx, dv, dotvirtv1alpha1.ConditionArgoReady, "ApplyFailed", err)
		}
	}
	r.setCondition(dv, dotvirtv1alpha1.ConditionArgoReady, metav1.ConditionTrue, "Ready", "argo resources applied")
	return nil, nil
}

// reconcileArgoWebhook sets up forge→ArgoCD instant sync: one ORG-level webhook
// covers every repo (present + future) with no per-repo registration. Skipped in
// dry-run (it mutates the forge + argocd-secret, which server-side dry-run can't
// model). A registration failure is recorded on the condition but doesn't halt
// the pipeline — Argo falls back to its poll.
func (r *DotvirtReconciler) reconcileArgoWebhook(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	if r.DryRun {
		r.setCondition(dv, dotvirtv1alpha1.ConditionArgoWebhook, metav1.ConditionUnknown, "DryRun", "skipped argo webhook in dry-run")
		return nil, nil
	}
	argoNS, _ := r.argoTarget(dv)
	if configured, err := r.ensureArgoWebhook(ctx, dv, argoNS); err != nil {
		r.setCondition(dv, dotvirtv1alpha1.ConditionArgoWebhook, metav1.ConditionFalse, "Error", err.Error())
	} else if configured {
		r.setCondition(dv, dotvirtv1alpha1.ConditionArgoWebhook, metav1.ConditionTrue, "Registered", "org webhook → ArgoCD")
	} else {
		r.setCondition(dv, dotvirtv1alpha1.ConditionArgoWebhook, metav1.ConditionUnknown, "NotRegistered", "ArgoCD webhook not registered (no Argo URL, or registration deferred); Argo falls back to its poll")
	}
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
	var src corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: install.AppsetSecretName}, &src); err != nil {
		return err // the source is ensured earlier in this reconcile
	}
	var existing corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: argoNS, Name: install.AppsetSecretName}, &existing)
	if err == nil {
		// One mirror name per ArgoCD namespace, so a second install sharing that namespace
		// would rewrite this one on every reconcile while the first rewrote it back, leaving
		// both plugin generators intermittently 401ing. Re-stamping the labels would also
		// pull the other install's Secret into this one's uninstall blast radius. Fail
		// loudly instead: the topology needs one ArgoCD namespace per install.
		if owner := existing.Labels[install.InstanceLabel]; owner != "" && owner != dv.Name {
			return fmt.Errorf("appset token mirror %s/%s belongs to dotvirt install %q; give each install its own ArgoCD namespace",
				argoNS, install.AppsetSecretName, owner)
		}
		if bytes.Equal(existing.Data["token"], src.Data["token"]) {
			return nil
		}
		existing.Data = map[string][]byte{"token": src.Data["token"]}
		// Stamp an adopted predecessor's mirror so it is cleaned up with this instance.
		existing.Labels = install.Labels(dv.Name)
		return r.Update(ctx, &existing)
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
	client, err := r.forgeClient(ctx, dv)
	if err != nil {
		return false, err
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

// ensureForgeTLSTrust merges the forge host + ingress CA into argocd-tls-certs-cm
// so repo-server VERIFIES the router cert. repo-creds templates silently ignore
// `insecure` (repository-secret-only field); without this entry the install only
// worked after an invisible hand-added cert. Managed-on-OpenShift only. MERGE:
// never touch other hosts' entries. Left on uninstall: not a secret, and a dead
// host key is inert.
func (r *DotvirtReconciler) ensureForgeTLSTrust(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, argoNS string) error {
	if !dv.Spec.Forge.Managed || r.Platform != platform.OpenShift {
		return nil
	}
	host := install.ForgejoHost(dv)
	if host == "" {
		return nil
	}
	var ingressCA corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: "openshift-config-managed", Name: "default-ingress-cert"}, &ingressCA); err != nil {
		// Legible and blocking: without trust every Application wedges in a
		// ComparisonError that says x509, three layers away from this cause.
		return fmt.Errorf("read default ingress CA (needed to trust the managed forge's route in ArgoCD): %w", err)
	}
	ca := ingressCA.Data["ca-bundle.crt"]
	if ca == "" {
		return fmt.Errorf("default-ingress-cert has no ca-bundle.crt")
	}
	// SSA with ONLY our host key: ConfigMap data keys get per-key field
	// ownership, so the merge cannot clobber the gitops operator's writes the way
	// a whole-object read-modify-write would race them. No instance labels: the
	// CM is shared, and labeling it would pull it into finalizer cleanup.
	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-tls-certs-cm", Namespace: argoNS},
		Data:       map[string]string{host: ca},
	}
	return install.Apply(ctx, r.Client, cm, false)
}
