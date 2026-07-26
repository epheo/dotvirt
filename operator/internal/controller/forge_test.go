package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
	"github.com/epheo/dotvirt/operator/internal/platform"
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
// writes carries the EXTERNAL forge URL: what the app, Argo, and browsers consume.
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

// reconcileForge is a silent no-op for a bring-your-own forge (URL set, not managed):
// the BYO path is handled entirely by the later secret/workload phases.
func TestReconcileForgeSilentForBYO(t *testing.T) {
	dv := testCR()
	dv.Spec.Forge.URL = "https://byo-forge.example"
	dv.Spec.Forge.CredentialsSecret = "byo-creds"
	dv.Status.ForgeAdminHint = "stale hint from a managed past"
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)

	res, err := r.reconcileForge(context.Background(), dv)
	if err != nil || res != nil {
		t.Fatalf("reconcileForge (BYO) = (%+v, %v), want (nil, nil)", res, err)
	}
	if cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionForgeReady) != nil {
		t.Error("ForgeReady set for a BYO forge; the phase must stay silent")
	}
	if dv.Status.ForgeURL != dv.Spec.Forge.URL {
		t.Errorf("status.forgeURL = %q, want the BYO url %q", dv.Status.ForgeURL, dv.Spec.Forge.URL)
	}
	if dv.Status.ForgeAdminHint != "" {
		t.Errorf("switching off managed must retire the bootstrap hint, got %q", dv.Status.ForgeAdminHint)
	}
}

// A URL alone is not a forge: the app reads both the URL and the token from the
// credentials secret, so the workload renders forge-less. The condition must name the
// missing credentialsSecret; a generic "no forge configured" beside a plainly set
// spec.forge.url reads as a lie.
func TestReconcileForgeURLWithoutCredential(t *testing.T) {
	dv := testCR()
	dv.Spec.Forge.URL = "https://byo-forge.example"
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)

	if _, err := r.reconcileForge(context.Background(), dv); err != nil {
		t.Fatalf("reconcileForge: %v", err)
	}
	fc := cond(dv, dotvirtv1alpha1.ConditionForgeReady)
	if fc == nil || fc.Reason != "CredentialsRequired" {
		t.Fatalf("ForgeReady = %+v, want False/CredentialsRequired", fc)
	}
}

// With no forge at all (not managed, no URL, no credentials secret), reconcileForge
// records NotConfigured (push-only) and proceeds; the workload then renders forge-less.
func TestReconcileForgeNotConfigured(t *testing.T) {
	dv := testCR() // wholly unconfigured forge
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)

	res, err := r.reconcileForge(context.Background(), dv)
	if err != nil || res != nil {
		t.Fatalf("reconcileForge = (%+v, %v), want (nil, nil)", res, err)
	}
	// NotConfigured is accumulated in-memory; the pipeline's final status write persists it.
	if fc := cond(dv, dotvirtv1alpha1.ConditionForgeReady); fc == nil || fc.Reason != "NotConfigured" {
		t.Errorf("ForgeReady = %+v, want False/NotConfigured", fc)
	}
}

func forgejoRoute(ns, host string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
	u.SetNamespace(ns)
	u.SetName(install.ForgejoServiceName)
	_ = unstructured.SetNestedField(u.Object, host, "spec", "host")
	return u
}

// End-to-end on OpenShift with no explicit URL: reconcileForge creates a HOSTLESS
// Forgejo Route (for the router to name) and requeues, waiting for the host; it does
// not dial anything or bootstrap yet.
func TestReconcileForgeCreatesHostlessRouteAndWaits(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.URL = ""
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)
	r.Platform = platform.OpenShift

	res, err := r.reconcileForge(context.Background(), dv)
	if err != nil {
		t.Fatalf("reconcileForge: %v", err)
	}
	if res == nil || res.RequeueAfter != 5*time.Second {
		t.Fatalf("result = %+v, want requeue 5s (waiting for the router host)", res)
	}
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: dv.Namespace, Name: install.ForgejoServiceName}, route); err != nil {
		t.Fatalf("hostless Forgejo Route not created: %v", err)
	}
	if host, _, _ := unstructured.NestedString(route.Object, "spec", "host"); host != "" {
		t.Errorf("Route host = %q, want empty (router-assigned)", host)
	}
	// Set right after the base apply, so it shows while provisioning, not only at Ready.
	if dv.Status.ForgeAdminHint == "" {
		t.Error("ForgeAdminHint not set during provisioning")
	}
}

// resolveForgeURL: an explicit spec.forge.url wins verbatim (trailing slash trimmed).
func TestResolveForgeURLExplicit(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.URL = "https://explicit.example/"
	r := newReconciler(testBuilder(t).WithObjects(dv).Build(), depsOK)

	got, res, err := r.resolveForgeURL(context.Background(), dv)
	if err != nil || res != nil {
		t.Fatalf("resolveForgeURL = (_, %+v, %v), want no halt", res, err)
	}
	if got != "https://explicit.example" {
		t.Errorf("url = %q, want the trimmed explicit url", got)
	}
}

// resolveForgeURL on OpenShift with no explicit URL derives it from the host the router
// assigned to the Forgejo Route.
func TestResolveForgeURLDerivesFromRouteOnOpenShift(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.URL = ""
	route := forgejoRoute(dv.Namespace, "dotvirt-forgejo-dotvirt.apps.cluster.example")
	r := newReconciler(testBuilder(t).WithObjects(dv, route).Build(), depsOK)
	r.Platform = platform.OpenShift

	got, res, err := r.resolveForgeURL(context.Background(), dv)
	if err != nil || res != nil {
		t.Fatalf("resolveForgeURL = (_, %+v, %v), want no halt", res, err)
	}
	if got != "https://dotvirt-forgejo-dotvirt.apps.cluster.example" {
		t.Errorf("url = %q, want the router-assigned host", got)
	}
}

// resolveForgeURL requeues (not halts) while the router hasn't assigned the host yet.
func TestResolveForgeURLRequeuesWhenHostPending(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.URL = ""
	r := newReconciler(testBuilder(t).WithObjects(dv).Build(), depsOK) // no Route object
	r.Platform = platform.OpenShift

	_, res, err := r.resolveForgeURL(context.Background(), dv)
	if err != nil {
		t.Fatalf("resolveForgeURL: %v", err)
	}
	if res == nil || res.RequeueAfter != 5*time.Second {
		t.Fatalf("result = %+v, want requeue 5s while the host is pending", res)
	}
}

// resolveForgeURL halts on vanilla Kubernetes with no explicit URL: there's no router to
// name the forge, so the user must set spec.forge.url.
func TestResolveForgeURLRequiresURLOnVanilla(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.URL = ""
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK) // Platform == Kubernetes

	_, res, err := r.resolveForgeURL(context.Background(), dv)
	if err != nil {
		t.Fatalf("resolveForgeURL: %v", err)
	}
	if res == nil || res.RequeueAfter != 0 {
		t.Fatalf("result = %+v, want a halt (no requeue)", res)
	}
	if fc := cond(getCR(t, c, dv), dotvirtv1alpha1.ConditionForgeReady); fc == nil || fc.Reason != "ForgeURLRequired" {
		t.Errorf("ForgeReady = %+v, want False/ForgeURLRequired", fc)
	}
}

// applyEffectiveForgeSpec fills the resolved URL and defaults the platform repo under it,
// but never overrides an explicit platformRepo.
func TestApplyEffectiveForgeSpec(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.URL = ""
	dv.Spec.Forge.PlatformRepo = ""
	applyEffectiveForgeSpec(dv, "https://forge.apps.example/")
	if dv.Spec.Forge.URL != "https://forge.apps.example/" {
		t.Errorf("url = %q, want the resolved url", dv.Spec.Forge.URL)
	}
	if dv.Spec.Forge.PlatformRepo != "https://forge.apps.example/dotvirt/platform.git" {
		t.Errorf("platformRepo = %q, want defaulted under the forge url", dv.Spec.Forge.PlatformRepo)
	}

	explicit := managedForgeCR()
	explicit.Spec.Forge.PlatformRepo = "https://forge.apps.example/team/custom.git"
	applyEffectiveForgeSpec(explicit, "https://forge.apps.example")
	if explicit.Spec.Forge.PlatformRepo != "https://forge.apps.example/team/custom.git" {
		t.Error("an explicit platformRepo must not be overridden")
	}
}

// forgeAdminHint returns a runnable retrieval command (user + secret + namespace),
// never the credential value: the password leaves only via the Secret it names.
func TestForgeAdminHint(t *testing.T) {
	hint := forgeAdminHint("dotvirt")
	for _, want := range []string{install.ForgejoBotUser, install.ForgejoAdminSecret, "-n dotvirt", "oc extract"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q missing %q", hint, want)
		}
	}
}

// The derived forge URL is filled in memory only: a status write (as the pipeline does)
// must never persist the effective spec back onto the stored CR.
func TestEffectiveForgeSpecNotPersisted(t *testing.T) {
	dv := managedForgeCR()
	dv.Spec.Forge.URL = ""
	dv.Spec.Forge.PlatformRepo = ""
	c := testBuilder(t).WithObjects(dv).Build()
	r := newReconciler(c, depsOK)

	applyEffectiveForgeSpec(dv, "https://forge.apps.example")
	dv.Status.Phase = dotvirtv1alpha1.PhaseReady
	if err := r.Status().Update(context.Background(), dv); err != nil {
		t.Fatalf("status update: %v", err)
	}
	stored := getCR(t, c, dv)
	if stored.Spec.Forge.URL != "" || stored.Spec.Forge.PlatformRepo != "" {
		t.Errorf("stored spec mutated: url=%q platformRepo=%q, both must stay empty",
			stored.Spec.Forge.URL, stored.Spec.Forge.PlatformRepo)
	}
}
