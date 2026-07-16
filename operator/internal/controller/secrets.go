package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

// reconcileSecrets ensures the generated secrets (create-once — never regenerated
// on re-reconcile, so the cookie key + plugin token survive restarts): the session
// key, the ApplicationSet plugin token, and the webhook secrets. The forge
// credential is supplied by the admin (spec.forge.credentialsSecret) or, earlier
// in the pipeline, by the managed-Forgejo bootstrap.
func (r *DotvirtReconciler) reconcileSecrets(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt) (*ctrl.Result, error) {
	if r.DryRun {
		return nil, nil
	}
	for _, s := range []struct{ name, key string }{
		{install.SessionSecretName, "secret"},
		{install.AppsetSecretName, "token"},
		{install.WebhookSecretName, "secret"},
		{install.ArgoWebhookSecretName, "secret"},
	} {
		if err := r.ensureSecret(ctx, dv, s.name, s.key); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// ensureSecret creates a labeled, owner-referenced Secret with a random value if it
// doesn't already exist. Create-once: an existing secret is never regenerated, so
// the session key / plugin token survive re-reconciles and restarts.
func (r *DotvirtReconciler) ensureSecret(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, name, key string) error {
	var existing corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: dv.Namespace, Name: name}, &existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	value, err := randomHex(32)
	if err != nil {
		return err
	}
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: dv.Namespace, Labels: install.Labels(dv.Name)},
		Data:       map[string][]byte{key: []byte(value)},
	}
	if err := controllerutil.SetControllerReference(dv, s, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, s)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
