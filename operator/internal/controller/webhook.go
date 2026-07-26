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

// reconcileDotvirtWebhook OBSERVES the forge->dotvirt instant-feedback webhook and
// reports it on the CR; it never registers it. The APP owns that hook: it re-asserts
// it on a runtime ticker (self-heal at a cadence the operator's event-driven
// reconcile can't match), covers user-owned forge owners repo by repo, and keeps
// working in operator-less deployments. A second writer here would re-create the
// standing two-sides-must-derive-identical-target coupling this observe-only split
// removes. (The Argo webhook stays operator-REGISTERED: it requires the privileged
// argocd-secret write the app must never hold.) Skipped in dry-run. Never halts the
// pipeline; the app's git poll backstops a missed hook either way.
func (r *DotvirtReconciler) reconcileDotvirtWebhook(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	if r.DryRun {
		r.setCondition(dv, dotvirtv1alpha1.ConditionDotvirtWebhook, metav1.ConditionUnknown, "DryRun", "skipped dotvirt webhook in dry-run")
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

// observeDotvirtWebhook reports whether an org-level hook delivering to dotvirt's
// /api/webhooks/forge endpoint exists. Read-only, best-effort: false with no error
// when there is nothing to observe yet (no forge / platform repo / delivery URL);
// err only when the forge cannot be asked (credentials, transport), which the
// condition surfaces as Unobservable rather than failing the install.
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
