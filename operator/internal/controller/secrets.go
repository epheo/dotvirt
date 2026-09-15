package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

// reconcileSecrets ensures the generated secrets (create-once - never regenerated
// on re-reconcile, so the cookie key + plugin token survive restarts): the session
// key, the ApplicationSet plugin token, and the webhook secrets. The forge
// credential is supplied by the admin (spec.forge.credentialsSecret) or, earlier
// in the pipeline, by the managed-Forgejo bootstrap.
func (r *DotvirtReconciler) reconcileSecrets(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	if r.DryRun {
		r.dryRunSkip(dv, dotvirtv1alpha1.ConditionSecretsReady, "secret generation")
		return nil, nil
	}
	secrets := []struct{ name, key string }{
		{install.SessionSecretName, "secret"},
		{install.AppsetSecretName, "token"},
		{install.WebhookSecretName, "secret"},
		{install.ArgoWebhookSecretName, "secret"},
	}
	if dv.Spec.Auth.OpenShiftSSO {
		// The generated OAuth client secret: the app reads it, and the admin copies it into
		// the OAuthClient via the status.ssoOAuthClient command (so it never lands in status).
		secrets = append(secrets, struct{ name, key string }{install.OAuthSecretName, "clientSecret"})
	}
	for _, s := range secrets {
		if err := r.ensureSecret(ctx, dv, s.name, s.key); err != nil {
			return nil, err
		}
	}
	r.setCondition(dv, dotvirtv1alpha1.ConditionSecretsReady, metav1.ConditionTrue, "Ready", "generated secrets present")
	return nil, nil
}

// ensureSecret applies a labeled, owner-referenced Secret with a random value if it
// doesn't already exist. Create-once: an existing secret is never regenerated, so
// the session key / plugin token survive re-reconciles and restarts.
func (r *DotvirtReconciler) ensureSecret(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, name, key string) error {
	if _, err := r.secret(ctx, dv.Namespace, name); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	value, err := randomHex(32)
	if err != nil {
		return err
	}
	return r.applyOwned(ctx, dv, &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: dv.Namespace, Labels: install.Labels(dv.Name)},
		Data:       map[string][]byte{key: []byte(value)},
	})
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
