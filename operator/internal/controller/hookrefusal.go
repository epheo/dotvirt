package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/pkg/forge"
)

// reasonRegistrationRefused: the forge declines the operator's forge account
// the webhooks it would register or observe; an admin creates the hook.
const reasonRegistrationRefused = "RegistrationRefused"

// reasonPerRepoHooks: the owner is a user account, so the app registers a hook
// per repo, which the operator does not observe.
const reasonPerRepoHooks = "PerRepoHooks"

// The webhook a refusal is remembered for.
const (
	hookDotvirt = "dotvirt"
	hookArgo    = "argo"
)

// hookRefusal is a forge's refusal of a webhook and what it was met with. The
// forge refuses again on every pass until something changes, so the next
// forge call waits until key (the forge, its owner, the credential, the hook)
// changes or the operator restarts, and the condition is restated from here.
type hookRefusal struct {
	key, reason, msg string
}

// heldRefusal returns the refusal of kind remembered for dv under key.
func (r *DotvirtReconciler) heldRefusal(dv *dotvirtv1alpha1.Dotvirt, kind, key string) (hookRefusal, bool) {
	v, ok := r.hookRefusals.Load(kind + "/" + string(dv.UID))
	if !ok {
		return hookRefusal{}, false
	}
	ref := v.(hookRefusal)
	return ref, ref.key == key
}

func (r *DotvirtReconciler) holdRefusal(dv *dotvirtv1alpha1.Dotvirt, kind, key, reason, msg string) hookRefusal {
	ref := hookRefusal{key: key, reason: reason, msg: msg}
	r.hookRefusals.Store(kind+"/"+string(dv.UID), ref)
	return ref
}

func (r *DotvirtReconciler) dropRefusal(dv *dotvirtv1alpha1.Dotvirt, kind string) {
	r.hookRefusals.Delete(kind + "/" + string(dv.UID))
}

// forgeAccess is forgeClient plus a key naming what the forge sees: its API
// base, the owner's platform repo and a fingerprint of the credential, never
// the credential itself.
func (r *DotvirtReconciler) forgeAccess(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*forge.Client, string, error) {
	c, token, base, err := r.forgeClientToken(ctx, dv)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{base, dv.Spec.Forge.PlatformRepo, token, fmt.Sprint(dv.Spec.Forge.InsecureTLS)}, "\x00")))
	return c, hex.EncodeToString(sum[:8]), nil
}

// fingerprint names a secret value in a refusal key without revealing it.
func fingerprint(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:6])
}

// refusedHookMessage tells an admin how to create the hook the forge refused
// to let the operator's account register or see: who refused, the hook in
// full, where its secret lives, and how to check it works.
func refusedHookMessage(dv *dotvirtv1alpha1.Dotvirt, owner string, status int, hookURL, events, secretName, also, fallback string) string {
	return fmt.Sprintf("the forge refused the operator's forge account the webhooks of %s (HTTP %d). "+
		"Create the hook by hand, as an org hook on %s, a hook on each project repo, or a system hook: "+
		"URL %s, content type application/json, events %s, secret the value of Secret %s/%s key \"secret\"%s. "+
		"The operator cannot observe deliveries: check the forge's delivery log for 2xx answers. %s "+
		"Asked again when spec.forge, the forge credential or the operator changes.",
		owner, status, owner, hookURL, events, dv.Namespace, secretName, also, fallback)
}

// dotvirtWebhookState is the DotvirtWebhook condition: the app registers the
// hook, the operator only observes it. A refusal of that read is remembered,
// so a held pass asks the forge nothing.
func (r *DotvirtReconciler) dotvirtWebhookState(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (metav1.ConditionStatus, string, string) {
	if dv.Spec.Forge.URL == "" || dv.Spec.Forge.PlatformRepo == "" {
		return notRegisteredDotvirtWebhook()
	}
	target := r.dotvirtWebhookTarget(dv)
	if target == "" {
		return notRegisteredDotvirtWebhook()
	}
	client, credKey, err := r.forgeAccess(ctx, dv)
	if credentialWaits(err) {
		return metav1.ConditionUnknown, reasonCredentialWait, credentialWaitMessage
	}
	if err != nil {
		return unobservableDotvirtWebhook(err)
	}
	hookURL := strings.TrimRight(target, "/") + "/api/webhooks/forge"
	key := credKey + "|" + hookURL
	if ref, held := r.heldRefusal(dv, hookDotvirt, key); held {
		return metav1.ConditionUnknown, ref.reason, ref.msg
	}
	registered, err := client.HasOrgWebhook(hookURL)
	if status := forge.RefusedStatus(err); status != 0 {
		// A user account has no org hooks: the app covers its repos one by one,
		// which the operator does not observe.
		if status == 404 {
			if isOrg, oerr := client.OwnerIsOrg(); oerr == nil && !isOrg {
				msg := fmt.Sprintf("%s is a user account, which has no org webhooks: the app registers a webhook on each project repo, which the operator does not observe; a project repo's webhook settings show it", client.Owner())
				return metav1.ConditionUnknown, reasonPerRepoHooks, r.holdRefusal(dv, hookDotvirt, key, reasonPerRepoHooks, msg).msg
			}
		}
		msg := refusedHookMessage(dv, client.Owner(), status, hookURL, "push and pull_request", install.WebhookSecretName, "",
			"Until then updates arrive via the app's git poll.")
		return metav1.ConditionUnknown, reasonRegistrationRefused, r.holdRefusal(dv, hookDotvirt, key, reasonRegistrationRefused, msg).msg
	}
	r.dropRefusal(dv, hookDotvirt)
	if err != nil {
		return unobservableDotvirtWebhook(err)
	}
	if registered {
		return metav1.ConditionTrue, "Registered", "org webhook -> dotvirt (registered by the app)"
	}
	return notRegisteredDotvirtWebhook()
}

func notRegisteredDotvirtWebhook() (metav1.ConditionStatus, string, string) {
	return metav1.ConditionUnknown, "NotRegistered",
		"the app registers this webhook within about a minute of starting; until then (or if its pod is down) updates arrive via the git poll instead of instantly"
}

func unobservableDotvirtWebhook(err error) (metav1.ConditionStatus, string, string) {
	return metav1.ConditionUnknown, "Unobservable",
		"could not read the forge's webhooks (" + err.Error() + "); updates still flow via the app's git poll"
}
