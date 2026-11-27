package controller

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/deps"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
)

// On OpenShift, openShiftSSO stays on, generates the OAuth client secret, and publishes
// the ready-to-apply OAuthClient command (redirect URI filled from the console host).
func TestReconcileSSOOpenShiftWiring(t *testing.T) {
	dv := testCR()
	dv.Spec.Auth.OpenShiftSSO = new(true)
	dv.Spec.Ingress.Host = "dotvirt.apps.cluster.example"
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)
	r.Platform = platform.OpenShift

	if _, err := r.reconcileSecrets(context.Background(), dv, phaseCtx(r, dv)); err != nil {
		t.Fatalf("reconcileSecrets: %v", err)
	}
	if !dv.Spec.Auth.SSOEnabled() {
		t.Fatal("SSO gated off on OpenShift; must stay on")
	}
	if !exists(t, c, &corev1.Secret{}, dv.Namespace, install.OAuthSecretName) {
		t.Error("OAuth client secret not generated")
	}
	if _, err := r.reconcileWorkload(context.Background(), dv, phaseCtx(r, dv)); err != nil {
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
// server): resolveSSO clears the flag in-memory and no client secret is generated.
func TestReconcileSSOGatedOffVanilla(t *testing.T) {
	dv := testCR()
	dv.Spec.Auth.OpenShiftSSO = new(true)
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK) // Platform == Kubernetes

	r.resolveSSO(dv, true)
	if _, err := r.reconcileSecrets(context.Background(), dv, phaseCtx(r, dv)); err != nil {
		t.Fatalf("reconcileSecrets: %v", err)
	}
	if dv.Spec.Auth.SSOEnabled() {
		t.Error("SSO must be gated off on vanilla Kubernetes")
	}
	if exists(t, c, &corev1.Secret{}, dv.Namespace, install.OAuthSecretName) {
		t.Error("no OAuth client secret should be generated off OpenShift")
	}
}

// The SSO gate is an effective-spec decision. A full reconcile on vanilla must
// leave spec.auth.openShiftSSO as the admin wrote it: the finalizer add is the
// one spec write, and normalizing before it persisted the gated-off value.
func TestReconcileDoesNotPersistNormalizedSpec(t *testing.T) {
	for name, set := range map[string]*bool{"true": new(true), "unset": nil} {
		dv := testCR()
		dv.Spec.Auth.OpenShiftSSO = set
		c := testBuilder(t).WithObjects(dv).Build()
		r := newReconciler(c, depsOK) // Platform == Kubernetes

		reconcileOnce(t, r, dv)

		if got := getCR(t, c, dv).Spec.Auth.OpenShiftSSO; (got == nil) != (set == nil) || got != nil && *got != *set {
			t.Errorf("%s: stored spec.auth.openShiftSSO = %v: the effective spec leaked into the CR", name, got)
		}
	}
}

// Derived status: toggling SSO off must retire the stale apply command.
func TestReconcileSSOToggleOffClearsStatus(t *testing.T) {
	dv := testCR()
	dv.Spec.Auth.OpenShiftSSO = new(false)
	dv.Spec.Ingress.Host = "dotvirt.apps.cluster.example"
	dv.Status.SSOOAuthClient = "oc apply -f - <<EOT ...stale... EOT"
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)
	r.Platform = platform.OpenShift

	if _, err := r.reconcileWorkload(context.Background(), dv, phaseCtx(r, dv)); err != nil {
		t.Fatalf("reconcileWorkload: %v", err)
	}
	if dv.Status.SSOOAuthClient != "" {
		t.Errorf("stale ssoOAuthClient must be cleared when SSO is off, got %q", dv.Status.SSOOAuthClient)
	}
}

// Unset follows the cluster: on where OpenShift's OAuth server runs and no oidc
// flow is set. An explicit value wins, except that SSO never runs off OpenShift.
func TestResolveSSO(t *testing.T) {
	oidc := &dotvirtv1alpha1.OIDCSpec{IssuerURL: "https://idm.example/oauth2/openid/dotvirt", ClientID: "dotvirt"}
	for _, c := range []struct {
		name        string
		plat        platform.Platform
		oauthServed bool
		set         *bool
		oidc        *dotvirtv1alpha1.OIDCSpec
		want        bool
	}{
		{"OpenShift, unset", platform.OpenShift, true, nil, nil, true},
		{"OpenShift, unset, oidc", platform.OpenShift, true, nil, oidc, false},
		{"OpenShift, false", platform.OpenShift, true, new(false), nil, false},
		{"OpenShift, false, oidc", platform.OpenShift, true, new(false), oidc, false},
		{"MicroShift, unset", platform.OpenShift, false, nil, nil, false},
		{"MicroShift, true", platform.OpenShift, false, new(true), nil, true},
		{"Kubernetes, unset", platform.Kubernetes, false, nil, nil, false},
		{"Kubernetes, true", platform.Kubernetes, false, new(true), nil, false},
	} {
		dv := testCR()
		dv.Spec.Auth = dotvirtv1alpha1.AuthSpec{OpenShiftSSO: c.set, OIDC: c.oidc}
		r := &DotvirtReconciler{Platform: c.plat}
		r.resolveSSO(dv, c.oauthServed)
		if got := dv.Spec.Auth.SSOEnabled(); got != c.want {
			t.Errorf("%s: SSO = %v, want %v", c.name, got, c.want)
		}
	}
}

// An install that never named openShiftSSO gets SSO on OpenShift from the
// dependency probe alone: client secret, app wiring and the apply command. One
// with oidc set keeps OIDC alone: no secret, no OAuth env, no command.
func TestReconcileSSODefault(t *testing.T) {
	oauth := func(*rest.Config) (deps.Result, error) { return deps.Result{OpenShiftOAuth: true}, nil }
	for _, withOIDC := range []bool{false, true} {
		dv := testCR()
		dv.Spec.Ingress.Host = "dotvirt.apps.cluster.example"
		if withOIDC {
			dv.Spec.Auth.OIDC = &dotvirtv1alpha1.OIDCSpec{
				IssuerURL: "https://idm.example/oauth2/openid/dotvirt", ClientID: "dotvirt",
				ClientSecret: &dotvirtv1alpha1.SecretKeyRef{Name: "dotvirt-login", Key: "client-secret"},
			}
		}
		c := testBuilder(t).WithObjects(dv).Build()
		r := newReconciler(c, oauth)
		r.Platform = platform.OpenShift
		rc := phaseCtx(r, dv)

		if _, err := r.reconcileDependencies(context.Background(), dv, rc); err != nil {
			t.Fatalf("reconcileDependencies: %v", err)
		}
		if _, err := r.reconcileSecrets(context.Background(), dv, rc); err != nil {
			t.Fatalf("reconcileSecrets: %v", err)
		}
		if _, err := r.reconcileWorkload(context.Background(), dv, rc); err != nil {
			t.Fatalf("reconcileWorkload: %v", err)
		}
		want := !withOIDC
		if got := exists(t, c, &corev1.Secret{}, dv.Namespace, install.OAuthSecretName); got != want {
			t.Errorf("oidc=%v: OAuth client secret present = %v, want %v", withOIDC, got, want)
		}
		if got := dv.Status.SSOOAuthClient != ""; got != want {
			t.Errorf("oidc=%v: status.ssoOAuthClient set = %v, want %v", withOIDC, got, want)
		}
		if got := ssoEnv(install.Deployment(dv, "argocd", nil)); got != want {
			t.Errorf("oidc=%v: app wired for OpenShift SSO = %v, want %v", withOIDC, got, want)
		}
		if stored := getCR(t, c, dv); stored.Spec.Auth.OpenShiftSSO != nil {
			t.Errorf("oidc=%v: the resolved openShiftSSO reached the stored CR", withOIDC)
		}
	}
}

func ssoEnv(dep *appsv1.Deployment) bool {
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "DOTVIRT_OAUTH_CLIENT_ID" {
			return true
		}
	}
	return false
}
