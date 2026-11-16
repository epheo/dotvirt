package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

// reconcileDotvirtWebhook OBSERVES the app-owned forge->dotvirt hook, never
// writes it: the app self-heals it on a ticker, covers user-owned forges repo by
// repo, and works operator-less. A second writer re-creates the both-sides-must-
// derive-the-same-target coupling. (The Argo hook stays operator-registered: it
// needs the argocd-secret write the app must never hold.) Never halts; the git
// poll backstops.
func (r *DotvirtReconciler) reconcileDotvirtWebhook(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, _ *reconcileCtx) (*ctrl.Result, error) {
	if credentialRefused(dv) {
		r.setCondition(dv, dotvirtv1alpha1.ConditionDotvirtWebhook, metav1.ConditionUnknown, reasonCredentialWait, credentialWaitMessage)
		return nil, nil
	}
	status, reason, msg := r.dotvirtWebhookState(ctx, dv)
	if status == metav1.ConditionUnknown && reason == "Unobservable" {
		logf.FromContext(ctx).Info("dotvirt webhook unobservable; git poll backstops", "message", msg)
	}
	r.setCondition(dv, dotvirtv1alpha1.ConditionDotvirtWebhook, status, reason, msg)
	return nil, nil
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
