package controller

import (
	"context"
	"strings"

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
func (r *DotvirtReconciler) reconcileDotvirtWebhook(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	if r.DryRun {
		r.dryRunSkip(dv, dotvirtv1alpha1.ConditionDotvirtWebhook, "dotvirt webhook")
		return nil, nil
	}
	if registered, err := r.observeDotvirtWebhook(ctx, dv); err != nil {
		r.setCondition(dv, dotvirtv1alpha1.ConditionDotvirtWebhook, metav1.ConditionUnknown, "Unobservable",
			"could not read the forge's webhooks ("+err.Error()+"); updates still flow via the app's git poll")
	} else if registered {
		r.setCondition(dv, dotvirtv1alpha1.ConditionDotvirtWebhook, metav1.ConditionTrue, "Registered", "org webhook -> dotvirt (registered by the app)")
	} else {
		r.setCondition(dv, dotvirtv1alpha1.ConditionDotvirtWebhook, metav1.ConditionUnknown, "NotRegistered",
			"the app registers this webhook within about a minute of starting; until then (or if its pod is down) updates arrive via the git poll instead of instantly")
	}
	return nil, nil
}

// observeDotvirtWebhook: read-only. false/no-error when there is nothing to
// observe yet; err only when the forge cannot be asked, surfaced as
// Unobservable, never an install failure.
func (r *DotvirtReconciler) observeDotvirtWebhook(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (bool, error) {
	if dv.Spec.Forge.URL == "" || dv.Spec.Forge.PlatformRepo == "" {
		return false, nil
	}
	target := r.dotvirtWebhookTarget(dv)
	if target == "" {
		return false, nil
	}
	client, err := r.forgeClient(ctx, dv)
	if err != nil {
		return false, err
	}
	registered, err := client.HasOrgWebhook(strings.TrimRight(target, "/") + "/api/webhooks/forge")
	if err != nil {
		logf.FromContext(ctx).Info("dotvirt webhook unobservable; git poll backstops", "error", err.Error())
		return false, err
	}
	return registered, nil
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
