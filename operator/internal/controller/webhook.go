package controller

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

// reconcileDotvirtWebhook sets up forge->dotvirt instant UI feedback: one ORG-level
// webhook covers every repo (the platform repo + all projects, present + future), so a
// push/merge refreshes dotvirt's inventory in webhook latency instead of the git-poll
// backstop. Symmetric with reconcileArgoWebhook. Registered at install time so a
// from-scratch install has the hook before any project namespace exists - the app's own
// per-project sweep never covered the platform repo and registered nothing until the first
// project. Skipped in dry-run. A registration failure is recorded on the condition but
// doesn't halt the pipeline - the app's git poll backstops a missed nudge.
func (r *DotvirtReconciler) reconcileDotvirtWebhook(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	if r.DryRun {
		r.setCondition(dv, dotvirtv1alpha1.ConditionDotvirtWebhook, metav1.ConditionUnknown, "DryRun", "skipped dotvirt webhook in dry-run")
		return nil, nil
	}
	if configured, err := r.ensureDotvirtWebhook(ctx, dv); err != nil {
		r.setCondition(dv, dotvirtv1alpha1.ConditionDotvirtWebhook, metav1.ConditionFalse, "Error", err.Error())
	} else if configured {
		r.setCondition(dv, dotvirtv1alpha1.ConditionDotvirtWebhook, metav1.ConditionTrue, "Registered", "org webhook -> dotvirt")
	} else {
		r.setCondition(dv, dotvirtv1alpha1.ConditionDotvirtWebhook, metav1.ConditionUnknown, "NotRegistered", "dotvirt webhook not registered (no forge URL / platform repo, or no delivery URL resolved yet); the git poll backstops updates")
	}
	return nil, nil
}

// ensureDotvirtWebhook registers one ORG-level forge webhook -> dotvirt's
// /api/webhooks/forge endpoint. Best-effort: returns configured=false (no error) when
// there's no org anchor (forge URL / platform repo), no reachable delivery URL yet, or the
// forge registration transiently fails (logged) - the app's poll backstops a missed hook,
// so none of those should fail the install. err is reserved for operator-internal failures
// (reading the webhook secret / forge credentials). Real-only (the caller skips it in
// dry-run) - it mutates the forge.
func (r *DotvirtReconciler) ensureDotvirtWebhook(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (configured bool, err error) {
	if dv.Spec.Forge.URL == "" || dv.Spec.Forge.PlatformRepo == "" {
		return false, nil
	}
	target := r.dotvirtWebhookTarget(dv)
	if target == "" {
		return false, nil
	}
	// The same secret the app validates deliveries with (DOTVIRT_WEBHOOK_SECRET): one
	// secret, both ends, so no mirroring is needed (unlike the Argo webhook's argocd-secret).
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: install.WebhookSecretName}, &s); err != nil {
		return false, err
	}
	value := string(s.Data["secret"])
	if value == "" {
		return false, nil
	}
	// One org webhook covers every repo, using the forge credential to register it. The
	// operator's client reaches a managed forge over its in-cluster API base, so this has no
	// hairpin problem the app's external-URL client can hit.
	client, err := r.forgeClient(ctx, dv)
	if err != nil {
		return false, err
	}
	// Registering the hook is best-effort, like reconcileArgoWebhook: a transient forge
	// hiccup must not read as a hard install error, because the git poll backstops a missed
	// nudge. Log it and report unconfigured so the next reconcile retries.
	if err := client.EnsureOrgWebhook(strings.TrimRight(target, "/")+"/api/webhooks/forge", value); err != nil {
		logf.FromContext(ctx).Info("dotvirt webhook registration deferred; git poll backstops", "error", err.Error())
		return false, nil
	}
	return true, nil
}

// dotvirtWebhookTarget resolves the base URL the forge delivers dotvirt webhooks to: a
// managed (in-cluster) Forgejo reaches the in-cluster Service (it can't hairpin the
// external Route, nor trust its CA); a bring-your-own forge is off-cluster and reaches the
// public Route host. Empty when a BYO forge has no resolved public host yet - the next
// reconcile retries once workload resolves it (Owns(Route)). Mirrors the app's webhookBase
// choice (WebhookURL else PublicURL).
func (r *DotvirtReconciler) dotvirtWebhookTarget(dv *dotvirtv1alpha1.Dotvirt) string {
	if dv.Spec.Forge.Managed {
		return install.ServiceURL(dv)
	}
	if dv.Spec.Ingress.Host != "" {
		return "https://" + dv.Spec.Ingress.Host
	}
	return ""
}
