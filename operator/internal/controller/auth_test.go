package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

// On OpenShift, openShiftSSO stays on, generates the OAuth client secret, and publishes
// the ready-to-apply OAuthClient command (redirect URI filled from the console host).
func TestReconcileSSOOpenShiftWiring(t *testing.T) {
	dv := testCR()
	dv.Spec.Auth.OpenShiftSSO = true
	dv.Spec.Ingress.Host = "dotvirt.apps.cluster.example"
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)
	r.Platform = platform.OpenShift

	if _, err := r.reconcileSecrets(context.Background(), dv); err != nil {
		t.Fatalf("reconcileSecrets: %v", err)
	}
	if !dv.Spec.Auth.OpenShiftSSO {
		t.Fatal("SSO gated off on OpenShift; must stay on")
	}
	if !exists(t, c, &corev1.Secret{}, dv.Namespace, install.OAuthSecretName) {
		t.Error("OAuth client secret not generated")
	}
	if _, err := r.reconcileWorkload(context.Background(), dv); err != nil {
		t.Fatalf("reconcileWorkload: %v", err)
	}
	cmd := dv.Status.SSOOAuthClient
	// One line: the console status view flattens newlines, so a heredoc dies on copy-paste.
	if strings.Contains(cmd, "\n") {
		t.Errorf("status.ssoOAuthClient must be a single line: %q", cmd)
	}
	if !strings.Contains(cmd, "OAuthClient") || !strings.Contains(cmd, "dotvirt.apps.cluster.example/api/auth/callback") {
		t.Errorf("status.ssoOAuthClient missing OAuthClient/redirect URI: %q", cmd)
	}
	if !strings.Contains(cmd, "oc apply") {
		t.Errorf("status.ssoOAuthClient must apply (create or repair) the OAuthClient: %q", cmd)
	}
	// The secret is read at apply time, never inlined into status.
	if !strings.Contains(cmd, "get secret "+install.OAuthSecretName) {
		t.Error("apply command must read the client secret from the Secret, not inline it")
	}
}

// Off OpenShift, SSO is silently disabled (the app's oauth flow needs the cluster oauth
// server): normalizeSpec clears the flag in-memory and no client secret is generated.
func TestReconcileSSOGatedOffVanilla(t *testing.T) {
	dv := testCR()
	dv.Spec.Auth.OpenShiftSSO = true
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK) // Platform == Kubernetes

	r.normalizeSpec(dv)
	if _, err := r.reconcileSecrets(context.Background(), dv); err != nil {
		t.Fatalf("reconcileSecrets: %v", err)
	}
	if dv.Spec.Auth.OpenShiftSSO {
		t.Error("SSO must be gated off on vanilla Kubernetes")
	}
	if exists(t, c, &corev1.Secret{}, dv.Namespace, install.OAuthSecretName) {
		t.Error("no OAuth client secret should be generated off OpenShift")
	}
}

// Derived status: toggling SSO off must retire the stale apply command.
func TestReconcileSSOToggleOffClearsStatus(t *testing.T) {
	dv := testCR()
	dv.Spec.Auth.OpenShiftSSO = false
	dv.Spec.Ingress.Host = "dotvirt.apps.cluster.example"
	dv.Status.SSOOAuthClient = "oc apply -f - <<EOT ...stale... EOT"
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)
	r.Platform = platform.OpenShift

	if _, err := r.reconcileWorkload(context.Background(), dv); err != nil {
		t.Fatalf("reconcileWorkload: %v", err)
	}
	if dv.Status.SSOOAuthClient != "" {
		t.Errorf("stale ssoOAuthClient must be cleared when SSO is off, got %q", dv.Status.SSOOAuthClient)
	}
}
