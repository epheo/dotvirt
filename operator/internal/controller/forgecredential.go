package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

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
	name := dv.Namespace + "/" + dv.Spec.Forge.CredentialsSecret
	switch {
	case errors.Is(err, errCredentialNameTaken):
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeCredentialReady, metav1.ConditionFalse, "NameTaken", fmt.Sprintf(
			"spec.forge.credentialsSecret names %s, a Secret the operator generates for itself: name another Secret; %s",
			name, credentialWaitEffects(dv)))
	case errors.Is(err, errCredentialNotGranted):
		use := "read"
		if dv.Spec.Forge.Managed {
			use = "read and write"
		}
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeCredentialReady, metav1.ConditionFalse, "NotGranted", fmt.Sprintf(
			"the operator may not %s Secret %s, which spec.forge.credentialsSecret names. A cluster admin grants it: %s; %s",
			use, name, strings.Join(install.ForgeCredentialCommands(dv, r.OperatorNamespace), " && "), credentialWaitEffects(dv)))
	case err == nil || apierrors.IsNotFound(err):
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeCredentialReady, metav1.ConditionTrue, "Granted",
			"the operator may use Secret "+name)
	}
}

// credentialWaitEffects says what waits on the credential meanwhile.
func credentialWaitEffects(dv *dotvirtv1alpha1.Dotvirt) string {
	if dv.Spec.Forge.Managed {
		return "until then no forge token is minted, and the platform repo and both webhooks wait; the ArgoCD tier keeps the repo credentials it has"
	}
	return "until then the platform repo, both webhooks and the ArgoCD tier wait: new projects, and namespaces added to a project, do not sync, and sync impersonation does not change"
}

// waitForCredential holds a bring-your-own forge's ArgoCD tier on the
// credential, whose url is the base tenant repo refs resolve against (the
// app's DOTVIRT_FORGE_URL), so no tier can be rendered without it. Nothing is
// applied or pruned meanwhile, so what an earlier pass rendered stays as it
// was; the phases after it (sync impersonation, the platform repo, both
// webhooks) do not run.
func (r *DotvirtReconciler) waitForCredential(dv *dotvirtv1alpha1.Dotvirt) *ctrl.Result {
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
	// The drift cadence, not sooner: a grant can stay pending for days. The
	// grant's RoleBinding is watched, so it landing (or going) triggers a pass.
	return &ctrl.Result{RequeueAfter: driftResync}
}
