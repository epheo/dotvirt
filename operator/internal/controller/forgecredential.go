package controller

import (
	"context"
	"errors"
	"fmt"

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
// install.ForgeCredentialGrant. The phases needing the credential wait on it;
// it never fails the pass.
var errCredentialNotGranted = errors.New("the forge credential is not granted to the operator")

// reasonCredentialNotGranted is the reason of every condition waiting on the grant.
const reasonCredentialNotGranted = "CredentialNotGranted"

// forgeSecret reads the forge credential; a refusal is errCredentialNotGranted.
func (r *DotvirtReconciler) forgeSecret(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*corev1.Secret, error) {
	s, err := r.secret(ctx, dv.Namespace, install.ForgeSecretName(dv))
	if apierrors.IsForbidden(err) {
		return nil, errCredentialNotGranted
	}
	return s, err
}

// recordCredentialGrant sets ForgeCredentialGranted from what using the
// credential returned: False while refused, True once a read or write went
// through (a missing Secret included: the read was allowed). Other errors say
// nothing about the grant and leave it as it was.
func (r *DotvirtReconciler) recordCredentialGrant(dv *dotvirtv1alpha1.Dotvirt, err error) {
	if !install.ForgeCredentialNeedsGrant(dv) {
		meta.RemoveStatusCondition(&dv.Status.Conditions, dotvirtv1alpha1.ConditionForgeCredentialGranted)
		return
	}
	name := dv.Namespace + "/" + dv.Spec.Forge.CredentialsSecret
	switch {
	case errors.Is(err, errCredentialNotGranted):
		use := "read"
		if dv.Spec.Forge.Managed {
			use = "read and write"
		}
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeCredentialGranted, metav1.ConditionFalse, "NotGranted", fmt.Sprintf(
			"the operator may not %s Secret %s, which spec.forge.credentialsSecret names: a cluster admin grants it from the console (Role and RoleBinding %s in %s); the forge's repo, its webhooks and the ArgoCD tier wait until then",
			use, name, install.ForgeCredentialGrantName, dv.Namespace))
	case err == nil || apierrors.IsNotFound(err):
		r.setCondition(dv, dotvirtv1alpha1.ConditionForgeCredentialGranted, metav1.ConditionTrue, "Granted",
			"the operator may use Secret "+name)
	}
}

// waitForCredential holds the forge-dependent phases on the grant: the ArgoCD
// tier (its repo credentials and tenant repos come from the credential), the
// platform repo and both webhooks. Nothing is applied or pruned meanwhile, so
// what an earlier pass rendered stays as it was.
func (r *DotvirtReconciler) waitForCredential(dv *dotvirtv1alpha1.Dotvirt) *ctrl.Result {
	msg := "waiting for " + dotvirtv1alpha1.ConditionForgeCredentialGranted
	conds := []string{dotvirtv1alpha1.ConditionArgoReady, dotvirtv1alpha1.ConditionArgoWebhook, dotvirtv1alpha1.ConditionDotvirtWebhook}
	if dv.Spec.Forge.PlatformRepo != "" {
		conds = append(conds, dotvirtv1alpha1.ConditionForgeRepoReady)
	}
	for _, c := range conds {
		r.setCondition(dv, c, metav1.ConditionUnknown, reasonCredentialNotGranted, msg)
	}
	if dv.Status.Phase == "" {
		dv.Status.Phase = dotvirtv1alpha1.PhaseProvisioning
	}
	// The drift cadence, not sooner: a grant can stay pending for days, and
	// neither Roles nor RoleBindings are watched.
	return &ctrl.Result{RequeueAfter: driftResync}
}
