package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

// errCredentialNotGranted: the operator may not use the Secret
// spec.forge.credentialsSecret names until a cluster admin installs
// install.ForgeCredentialGrant.
var errCredentialNotGranted = errors.New("the forge credential is not granted to the operator")

// errCredentialNameTaken: spec.forge.credentialsSecret names a Secret the
// operator generates for itself, which the credential would overwrite.
var errCredentialNameTaken = errors.New("spec.forge.credentialsSecret names one of the operator's own Secrets")

// credentialWaits reports whether err is the credential being unusable as
// configured: the phases needing it wait, and it never fails the pass.
func credentialWaits(err error) bool {
	return errors.Is(err, errCredentialNotGranted) || errors.Is(err, errCredentialNameTaken)
}

// reasonCredentialWait is the reason of every condition waiting on the credential.
const reasonCredentialWait = "WaitingForCredential"

// credentialWaitMessage is the message of every condition waiting on the credential.
const credentialWaitMessage = "waiting for " + dotvirtv1alpha1.ConditionForgeCredentialReady

// grantLagRetry is how soon a pass comes back while a freshly changed grant is
// still refused: the apiserver's RBAC caches lag a new binding briefly, and no
// further event arrives once they catch up.
const grantLagRetry = time.Minute

// grantLagWindow is how long after its newest change a grant that is present
// but still refused reads as the apiserver catching up. Past it the grant is
// wrong, and the pass keeps the drift cadence: the Role and RoleBinding watches
// still catch the admin's fix.
const grantLagWindow = 5 * time.Minute

// observeGrant refines a refused credential (this pass recorded NotGranted) by
// what a cluster admin installed: when the Role or RoleBinding is there but
// does not grant what the operator needs, the condition says so and how to
// replace them. It reports whether that grant changed within grantLagWindow.
// Two direct GETs, only while the credential is refused.
func (r *DotvirtReconciler) observeGrant(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) bool {
	c := meta.FindStatusCondition(dv.Status.Conditions, dotvirtv1alpha1.ConditionForgeCredentialReady)
	if c == nil || c.Reason != "NotGranted" {
		return false
	}
	key := types.NamespacedName{Namespace: dv.Namespace, Name: install.ForgeCredentialGrantName}
	var present []string
	var newest time.Time
	for _, obj := range []client.Object{&rbacv1.Role{}, &rbacv1.RoleBinding{}} {
		if r.reader().Get(ctx, key, obj) != nil {
			continue
		}
		present = append(present, strings.TrimPrefix(fmt.Sprintf("%T", obj), "*v1."))
		newest = latestChange(obj, newest)
	}
	if len(present) == 0 {
		return false
	}
	landing := r.clock().Sub(newest) < grantLagWindow
	exist := "exists but does"
	if len(present) == 2 {
		exist = "exist but do"
	}
	recheck := ""
	if landing {
		recheck = " (changed under " + grantLagWindow.String() + " ago: rechecked every minute while the apiserver applies it)"
	}
	r.setCondition(dv, dotvirtv1alpha1.ConditionForgeCredentialReady, metav1.ConditionFalse, "GrantIncomplete", fmt.Sprintf(
		"%s %s in the install namespace %s not grant the operator's account %s on the Secret spec.forge.credentialsSecret names%s. %s; %s",
		strings.Join(present, " and "), install.ForgeCredentialGrantName, exist,
		strings.Join(install.ForgeCredentialVerbs(dv), ", "), recheck, credentialNextAction("replaces them"), credentialWaitEffects(dv)))
	return landing
}

// latestChange is the newest of after and obj's creation and field updates.
func latestChange(obj client.Object, after time.Time) time.Time {
	if t := obj.GetCreationTimestamp().Time; t.After(after) {
		after = t
	}
	for _, f := range obj.GetManagedFields() {
		if f.Time != nil && f.Time.After(after) {
			after = f.Time.Time
		}
	}
	return after
}

// mayStoreCredential asks, for an admin-named credential, whether the operator
// may write it before a token is minted: one minted but never stored is a
// dangling forge token every pass. The write is a server-side apply, which
// needs patch always and create too while the Secret is absent.
func (r *DotvirtReconciler) mayStoreCredential(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, present bool) error {
	if !install.ForgeCredentialNeedsGrant(dv) {
		return nil
	}
	verbs := []string{"patch"}
	if !present {
		verbs = []string{"create", "patch"}
	}
	for _, verb := range verbs {
		review := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: dv.Namespace, Verb: verb, Resource: "secrets", Name: dv.Spec.Forge.CredentialsSecret,
			},
		}}
		if err := r.Create(ctx, review); err != nil {
			return fmt.Errorf("ask whether the operator may %s Secret %s/%s: %w", verb, dv.Namespace, dv.Spec.Forge.CredentialsSecret, err)
		}
		if !review.Status.Allowed {
			return errCredentialNotGranted
		}
	}
	return nil
}

// forgeSecret reads the forge credential: a refusal is errCredentialNotGranted,
// and a name the operator's own Secrets hold is errCredentialNameTaken, never read.
func (r *DotvirtReconciler) forgeSecret(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*corev1.Secret, error) {
	if install.ForgeCredentialNameTaken(dv) {
		return nil, errCredentialNameTaken
	}
	s, err := r.secret(ctx, dv.Namespace, install.ForgeSecretName(dv))
	if apierrors.IsForbidden(err) {
		return nil, errCredentialNotGranted
	}
	return s, err
}

// credentialRefused reports whether this pass found the credential unusable
// (ForgeCredentialReady False, any reason): the phases after the forge's then
// wait on it without reading it.
func credentialRefused(dv *dotvirtv1alpha1.Dotvirt) bool {
	return meta.IsStatusConditionFalse(dv.Status.Conditions, dotvirtv1alpha1.ConditionForgeCredentialReady)
}

// credentialChecked reports whether ForgeCredentialReady applies: any
// credential name but the operator's own default.
func credentialChecked(dv *dotvirtv1alpha1.Dotvirt) bool {
	return install.ForgeCredentialNeedsGrant(dv) || install.ForgeCredentialNameTaken(dv)
}

// recordCredential sets ForgeCredentialReady from what using the credential
// returned: False while it is refused or its name is taken, True once a read
// or write went through (a missing Secret included: the read was allowed).
// Other errors say nothing about the grant and leave it as it was.
func (r *DotvirtReconciler) recordCredential(dv *dotvirtv1alpha1.Dotvirt, err error) {
	if !credentialChecked(dv) {
		meta.RemoveStatusCondition(&dv.Status.Conditions, dotvirtv1alpha1.ConditionForgeCredentialReady)
		return
	}
	switch {
	case errors.Is(err, errCredentialNameTaken):
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeCredentialReady, metav1.ConditionFalse, "NameTaken",
			"spec.forge.credentialsSecret names a Secret the operator generates for itself: name another Secret; "+credentialWaitEffects(dv))
	case errors.Is(err, errCredentialNotGranted):
		use := "read"
		if dv.Spec.Forge.Managed {
			use = "read and write"
		}
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeCredentialReady, metav1.ConditionFalse, "NotGranted", fmt.Sprintf(
			"the operator may not %s the Secret spec.forge.credentialsSecret names. %s the operator's account %s on it; %s",
			use, credentialNextAction("grants"), strings.Join(install.ForgeCredentialVerbs(dv), ", "), credentialWaitEffects(dv)))
	case err == nil || apierrors.IsNotFound(err):
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeCredentialReady, metav1.ConditionTrue, "Granted",
			"the operator may use the Secret spec.forge.credentialsSecret names")
	}
}

// credentialNextAction is what a cluster admin does about the grant. It names
// no value and gives no command: the operator writes this message, so an admin
// derives each value from the cluster, as the README's commands show.
func credentialNextAction(does string) string {
	return "A cluster admin " + does + " from the console (Install), or with the operator README's \"Forge credential grant\" commands, each value derived from the install's spec and the operator's Deployment"
}

// credentialWaitEffects says what waits on the credential meanwhile.
func credentialWaitEffects(dv *dotvirtv1alpha1.Dotvirt) string {
	if dv.Spec.Forge.Managed {
		return "until then no forge token is minted or replaced, and the platform repo and both webhooks wait; the ArgoCD tier keeps the repo credentials it has, and after a move to another ArgoCD namespace has none there"
	}
	return "until then the platform repo, both webhooks and the ArgoCD tier wait: new projects, and namespaces added to a project, do not sync, and sync impersonation does not change"
}

// waitForCredential holds a bring-your-own forge's ArgoCD tier on the
// credential, whose url is the base tenant repo refs resolve against (the
// app's DOTVIRT_FORGE_URL), so no tier can be rendered without it. Nothing is
// applied or pruned meanwhile, so what an earlier pass rendered stays as it
// was; the phases after it (sync impersonation, the platform repo, both
// webhooks) do not run.
func (r *DotvirtReconciler) waitForCredential(dv *dotvirtv1alpha1.Dotvirt, rc *reconcileCtx) *ctrl.Result {
	conds := []string{
		dotvirtv1alpha1.ConditionArgoReady, dotvirtv1alpha1.ConditionSyncImpersonationReady,
		dotvirtv1alpha1.ConditionArgoWebhook, dotvirtv1alpha1.ConditionDotvirtWebhook,
	}
	if dv.Spec.Forge.PlatformRepo != "" {
		conds = append(conds, dotvirtv1alpha1.ConditionForgeRepoReady)
	}
	for _, c := range conds {
		r.setCondition(dv, c, metav1.ConditionUnknown, reasonCredentialWait, credentialWaitMessage)
	}
	if dv.Status.Phase == "" {
		dv.Status.Phase = dotvirtv1alpha1.PhaseProvisioning
	}
	// The drift cadence, not sooner: a grant can stay pending for days. Its
	// Role and RoleBinding are watched, so it landing (or going) triggers a
	// pass; one changed within grantLagWindow but still refused is RBAC-cache lag.
	if rc.grantLanding {
		return &ctrl.Result{RequeueAfter: grantLagRetry}
	}
	return &ctrl.Result{RequeueAfter: driftResync}
}
