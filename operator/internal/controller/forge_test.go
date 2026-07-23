package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/pkg/forge"
)

// managedForgeCR is a CR requesting the operator-hosted eval Forgejo, with an external
// URL the app/Argo/browsers use. The operator reaches the forge over the in-cluster
// Service (the test seam), never this URL.
func managedForgeCR() *dotvirtv1alpha1.Dotvirt {
	dv := testCR()
	dv.Spec.Forge = dotvirtv1alpha1.ForgeSpec{
		Managed:      true,
		URL:          "https://forgejo.apps.test.example",
		PlatformRepo: "https://forgejo.apps.test.example/dotvirt/platform.git",
	}
	return dv
}

func getSecret(t *testing.T, r *DotvirtReconciler, ns, name string) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		t.Fatalf("get secret %s/%s: %v", ns, name, err)
	}
	return &s
}

// The bootstrap mints over the in-cluster Service (the seam), but the credential it
// writes carries the EXTERNAL forge URL — what the app, Argo, and browsers consume.
func TestBootstrapForgejoMintsViaServiceURLWritesExternalURL(t *testing.T) {
	dv := managedForgeCR()
	var sawTokenPOST bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/tokens/"):
			w.WriteHeader(http.StatusNotFound) // no prior token of this name
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens"):
			sawTokenPOST = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sha1":"tok123"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/dotvirt":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`)) // org already exists
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	readyForgejo := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: install.ForgejoServiceName, Namespace: dv.Namespace},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1},
	}
	admin := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: install.ForgejoAdminSecret, Namespace: dv.Namespace},
		Data:       map[string][]byte{"password": []byte("adminpw")},
	}
	c := testBuilder(t).WithObjects(dv, readyForgejo, admin).Build()
	r := newReconciler(c, depsOK)
	r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }

	ready, err := r.bootstrapForgejo(context.Background(), dv)
	if err != nil || !ready {
		t.Fatalf("bootstrapForgejo: ready=%v err=%v", ready, err)
	}
	if !sawTokenPOST {
		t.Error("token was not minted over the Service URL")
	}
	cred := getSecret(t, r, dv.Namespace, install.DefaultForgeSecret)
	if got := string(cred.Data["url"]); got != dv.Spec.Forge.URL {
		t.Errorf("forge secret url = %q, want the external URL %q", got, dv.Spec.Forge.URL)
	}
	if got := string(cred.Data["username"]); got != install.ForgejoBotUser {
		t.Errorf("forge secret username = %q, want %q", got, install.ForgejoBotUser)
	}
	if got := string(cred.Data["token"]); got != "tok123" {
		t.Errorf("forge secret token = %q, want tok123", got)
	}
}

// A url edit after a successful bootstrap must reach the credential secret even though
// the token still validates (the secret's url is otherwise written only at mint time),
// and must NOT trigger a re-mint.
func TestBootstrapForgejoSelfHealsSecretURLWithoutReMint(t *testing.T) {
	dv := managedForgeCR()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/user" {
			w.WriteHeader(http.StatusOK) // stored token still authenticates
			return
		}
		t.Errorf("unexpected %s %s (no re-mint expected)", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	stale := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: install.DefaultForgeSecret, Namespace: dv.Namespace},
		Data: map[string][]byte{
			"url":      []byte("https://old-forge.example"),
			"username": []byte(install.ForgejoBotUser),
			"token":    []byte("good-token"),
		},
	}
	c := testBuilder(t).WithObjects(dv, stale).Build()
	r := newReconciler(c, depsOK)
	r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }

	ready, err := r.bootstrapForgejo(context.Background(), dv)
	if err != nil || !ready {
		t.Fatalf("bootstrapForgejo: ready=%v err=%v", ready, err)
	}
	cred := getSecret(t, r, dv.Namespace, install.DefaultForgeSecret)
	if got := string(cred.Data["url"]); got != dv.Spec.Forge.URL {
		t.Errorf("forge secret url = %q, want healed to %q", got, dv.Spec.Forge.URL)
	}
	if got := string(cred.Data["token"]); got != "good-token" {
		t.Errorf("token = %q, want the original good-token (no re-mint)", got)
	}
}

// A rejected admin password (Forgejo data volume predating the current admin secret)
// surfaces as ErrUnauthorized, which reconcileForge maps to a distinct, actionable
// condition rather than a generic error.
func TestBootstrapForgejoAdminMismatchReturnsErrUnauthorized(t *testing.T) {
	dv := managedForgeCR()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // basic-auth rejected on every admin call
	}))
	defer srv.Close()

	readyForgejo := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: install.ForgejoServiceName, Namespace: dv.Namespace},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1},
	}
	admin := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: install.ForgejoAdminSecret, Namespace: dv.Namespace},
		Data:       map[string][]byte{"password": []byte("wrong")},
	}
	c := testBuilder(t).WithObjects(dv, readyForgejo, admin).Build()
	r := newReconciler(c, depsOK)
	r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }

	_, err := r.bootstrapForgejo(context.Background(), dv)
	if !errors.Is(err, forge.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

// reconcileForge is a no-op for a bring-your-own forge: it must not touch the cluster
// or the conditions (the BYO path is handled entirely by the later secret/workload phases).
func TestReconcileForgeSkipsWhenNotManaged(t *testing.T) {
	dv := testCR() // Forge.Managed == false
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)

	res, err := r.reconcileForge(context.Background(), dv)
	if err != nil || res != nil {
		t.Fatalf("reconcileForge (BYO) = (%+v, %v), want (nil, nil)", res, err)
	}
	if cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionForgeReady) != nil {
		t.Error("ForgeReady set for a BYO forge; the phase must stay silent")
	}
}
