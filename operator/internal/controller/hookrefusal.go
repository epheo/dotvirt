package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Reasons of a webhook the forge will not let the operator's account register
// or see.
const (
	// reasonRegistrationRefused: the forge declines the account the owner's
	// webhooks; an admin grants it or creates the hook.
	reasonRegistrationRefused = "RegistrationRefused"
	// reasonCredentialRejected: the forge rejects the token itself (401).
	reasonCredentialRejected = "CredentialRejected"
	// reasonPerRepoHooks: the owner is a user account, which has no org hooks.
	reasonPerRepoHooks = "PerRepoHooks"
	// reasonOwnerNotFound: no org or user carries the platform repo's owner.
	reasonOwnerNotFound = "OwnerNotFound"
)

// refusalRecheck is how long a refusal is held: a fix made in the forge itself
// (the account made an owner, webhooks re-enabled) changes nothing the
// operator sees, so the forge is asked again at this pace.
const refusalRecheck = time.Hour

// The webhook a refusal is remembered for.
const (
	hookDotvirt = "dotvirt"
	hookArgo    = "argo"
)

// hookRefusal is a forge's refusal of a webhook, what it was met with and
// when. The forge refuses again on every pass, so the next forge call waits
// until key (the forge, its owner, the credential, the hook) changes,
// refusalRecheck passes or the operator restarts, and the condition is
// restated from here.
type hookRefusal struct {
	key, reason, msg string
	at               time.Time
}

// heldRefusal returns the refusal of kind held for dv under key.
func (r *DotvirtReconciler) heldRefusal(dv *dotvirtv1alpha1.Dotvirt, kind, key string) (hookRefusal, bool) {
	v, ok := r.hookRefusals.Load(kind + "/" + string(dv.UID))
	if !ok {
		return hookRefusal{}, false
	}
	ref := v.(hookRefusal)
	return ref, ref.key == key && r.clock().Sub(ref.at) < refusalRecheck
}

func (r *DotvirtReconciler) holdRefusal(dv *dotvirtv1alpha1.Dotvirt, kind, key, reason, msg string) (metav1.ConditionStatus, string, string) {
	r.hookRefusals.Store(kind+"/"+string(dv.UID), hookRefusal{key: key, reason: reason, msg: msg, at: r.clock()})
	return metav1.ConditionUnknown, reason, msg
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

// hookSpec is a webhook as an admin would create it by hand, and how its
// refusal reads.
type hookSpec struct {
	kind, url, events string
	// secret is the Secret, in the install namespace, holding the shared
	// secret under key "secret"; also names where else that value lives.
	secret, also string
	// changes names what, beside the forge and its credential, asks again;
	// fallback is what keeps working meanwhile.
	changes, fallback string
	// perRepo is the advice for a user-owned forge, which has no org hooks.
	perRepo string
}

// refused classifies the forge's refusal err of hook for owner and holds it
// under key: a rejected token, a user or missing owner (a 404 on org hooks
// asks which, once), or the account lacking the owner's webhooks.
func (r *DotvirtReconciler) refused(dv *dotvirtv1alpha1.Dotvirt, fc *forge.Client, err error, key string, hook hookSpec) (metav1.ConditionStatus, string, string) {
	status := forge.RefusedStatus(err)
	owner := fc.Owner()
	recheck := "Rechecked hourly, and at once when spec.forge, the forge credential" + hook.changes + " changes, or the operator restarts."
	if status == http.StatusNotFound {
		isOrg, oerr := fc.OwnerIsOrg()
		if oerr == nil && !isOrg {
			isUser, uerr := fc.OwnerIsUser()
			switch {
			case uerr == nil && isUser:
				return r.holdRefusal(dv, hook.kind, key, reasonPerRepoHooks, fmt.Sprintf(
					"%s is a user account, which has no org webhooks: %s %s", owner, hook.perRepo, recheck))
			case uerr == nil:
				return r.holdRefusal(dv, hook.kind, key, reasonOwnerNotFound, fmt.Sprintf(
					"the forge has no org or user %q, the owner of spec.forge.platformRepo: check spec.forge.platformRepo and spec.forge.url. %s",
					owner, recheck))
			}
			oerr = uerr
		}
		if oerr != nil && forge.RefusedStatus(oerr) != 0 {
			err, status = oerr, forge.RefusedStatus(oerr)
		}
	}
	reason := forge.RefusalText(err)
	if reason == "" {
		reason = http.StatusText(status)
	}
	if status == http.StatusUnauthorized {
		return r.holdRefusal(dv, hook.kind, key, reasonCredentialRejected, fmt.Sprintf(
			"the forge rejected the operator's forge credential (HTTP 401: %s): replace the token in Secret %s/%s key token. %s",
			reason, dv.Namespace, install.ForgeSecretName(dv), recheck))
	}
	return r.holdRefusal(dv, hook.kind, key, reasonRegistrationRefused, fmt.Sprintf(
		"the forge refused the operator's forge account the webhooks of %s (HTTP %d: %s). "+
			"Make that account an owner of the org, or create the hook by hand as a Forgejo (Gitea) webhook: "+
			"an org hook on %s, a system hook, or a hook on each project repo (repos created later need their own): "+
			"URL %s, content type application/json, events %s, secret the value of Secret %s/%s key \"secret\" "+
			"(oc extract secret/%s -n %s --keys=secret --to=-)%s. "+
			"The operator cannot observe deliveries: check the forge's delivery log for 2xx answers. %s %s",
		owner, status, reason, owner, hook.url, hook.events, dv.Namespace, hook.secret, hook.secret, dv.Namespace,
		hook.also, hook.fallback, recheck))
}

// dotvirtWebhookState is the DotvirtWebhook condition: the app registers the
// hook, the operator only observes it. A refusal of that read is held, so a
// held pass asks the forge nothing.
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
	if forge.Refused(err) {
		return r.refused(dv, client, err, key, hookSpec{
			kind: hookDotvirt, url: hookURL, events: "push and pull_request", secret: install.WebhookSecretName,
			fallback: "Until then updates arrive via the app's git poll.",
			perRepo: "the app registers a webhook on each project repo, which the operator does not observe; " +
				"a project repo's webhook settings show it.",
		})
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
		"the app registers this webhook within about a minute of starting, and asks again hourly after a refusal; until then (or if its pod is down) updates arrive via the git poll instead of instantly"
}

func unobservableDotvirtWebhook(err error) (metav1.ConditionStatus, string, string) {
	return metav1.ConditionUnknown, "Unobservable",
		"could not read the forge's webhooks (" + err.Error() + "); updates still flow via the app's git poll"
}
