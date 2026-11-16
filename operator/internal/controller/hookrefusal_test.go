package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

// hookForge answers the org's hooks, and the org lookup, with the given
// statuses and counts every call.
type hookForge struct {
	hooks, org atomic.Int32
	calls      atomic.Int32
}

func (f *hookForge) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		switch r.URL.Path {
		case "/api/v1/orgs/dotvirt/hooks":
			if s := int(f.hooks.Load()); s != http.StatusOK {
				w.WriteHeader(s)
				return
			}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":1}`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case "/api/v1/orgs/dotvirt":
			w.WriteHeader(int(f.org.Load()))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// hookSetup builds a managed-forge install whose forge is f, with the forge
// credential and the Argo webhook secret in place.
func hookSetup(t *testing.T, f *hookForge) (*DotvirtReconciler, client.Client, *dotvirtv1alpha1.Dotvirt) {
	t.Helper()
	srv := f.serve(t)
	dv := managedForgeCR()
	dv.UID = "install-uid"
	dv.Spec.ArgoCD.ServerURL = "https://argocd.example"
	c := testBuilder(t).WithObjects(dv,
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: install.ForgeSecretName(dv), Namespace: dv.Namespace},
			Data:       map[string][]byte{"url": []byte(dv.Spec.Forge.URL), "token": []byte("one")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: install.ArgoWebhookSecretName, Namespace: dv.Namespace},
			Data:       map[string][]byte{"secret": []byte("hmac")},
		},
	).Build()
	r := newReconciler(c, depsOK)
	r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }
	return r, c, dv
}

func setToken(t *testing.T, c client.Client, dv *dotvirtv1alpha1.Dotvirt, name, key, value string) {
	t.Helper()
	s := &corev1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: dv.Namespace, Name: name}, s); err != nil {
		t.Fatal(err)
	}
	s.Data[key] = []byte(value)
	if err := c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// A forge refusing the operator's account the org's webhooks gets the hook to
// create by hand, and is not asked again until the credential changes.
func TestDotvirtWebhookRefusalIsGuidedAndHeld(t *testing.T) {
	f := &hookForge{}
	f.hooks.Store(http.StatusForbidden)
	r, c, dv := hookSetup(t, f)
	pass := func() *metav1.Condition {
		if _, err := r.reconcileDotvirtWebhook(context.Background(), dv, nil); err != nil {
			t.Fatal(err)
		}
		return cond(dv, dotvirtv1alpha1.ConditionDotvirtWebhook)
	}

	got := pass()
	if got == nil || got.Status != metav1.ConditionUnknown || got.Reason != reasonRegistrationRefused {
		t.Fatalf("DotvirtWebhook = %+v, want Unknown/%s", got, reasonRegistrationRefused)
	}
	for _, want := range []string{"/api/webhooks/forge", "push and pull_request", "application/json",
		dv.Namespace + "/" + install.WebhookSecretName, "system hook", "delivery log"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message lacks %q: %s", want, got.Message)
		}
	}
	if strings.Contains(got.Message, "one") {
		t.Errorf("message carries the credential: %s", got.Message)
	}
	asked := f.calls.Load()
	pass()
	pass()
	if f.calls.Load() != asked {
		t.Errorf("held passes made %d forge calls, want none", f.calls.Load()-asked)
	}
	if got := pass(); got.Reason != reasonRegistrationRefused {
		t.Errorf("held pass reason %s, want %s", got.Reason, reasonRegistrationRefused)
	}

	setToken(t, c, dv, install.ForgeSecretName(dv), "token", "two")
	f.hooks.Store(http.StatusOK)
	if got := pass(); got.Reason != "NotRegistered" {
		t.Errorf("after a credential change: %+v, want the forge asked again (NotRegistered)", got)
	}
}

// An outage is not a refusal: every pass asks again.
func TestDotvirtWebhookOutageRetries(t *testing.T) {
	f := &hookForge{}
	f.hooks.Store(http.StatusBadGateway)
	r, _, dv := hookSetup(t, f)
	for range 3 {
		if _, err := r.reconcileDotvirtWebhook(context.Background(), dv, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := cond(dv, dotvirtv1alpha1.ConditionDotvirtWebhook); got == nil || got.Reason != "Unobservable" {
		t.Errorf("DotvirtWebhook = %+v, want Unobservable", got)
	}
	if f.calls.Load() != 3 {
		t.Errorf("%d forge calls over three passes, want one each", f.calls.Load())
	}
}

// A user account has no org hooks: the app covers its repos one by one, which
// the operator says rather than calling it a refusal.
func TestDotvirtWebhookUserOwnerIsPerRepo(t *testing.T) {
	f := &hookForge{}
	f.hooks.Store(http.StatusNotFound)
	f.org.Store(http.StatusNotFound)
	r, _, dv := hookSetup(t, f)
	if _, err := r.reconcileDotvirtWebhook(context.Background(), dv, nil); err != nil {
		t.Fatal(err)
	}
	if got := cond(dv, dotvirtv1alpha1.ConditionDotvirtWebhook); got == nil || got.Reason != reasonPerRepoHooks {
		t.Errorf("DotvirtWebhook = %+v, want %s", got, reasonPerRepoHooks)
	}
}

// The ArgoCD hook: a refusal names the push-only hook and both places its
// secret lives, and is held until the secret, the credential or the Argo URL
// changes; an outage keeps retrying.
func TestArgoWebhookRefusalIsGuidedAndHeld(t *testing.T) {
	f := &hookForge{}
	f.hooks.Store(http.StatusForbidden)
	r, c, dv := hookSetup(t, f)
	pass := func() *metav1.Condition {
		if _, err := r.reconcileArgoWebhook(context.Background(), dv, phaseCtx(r, dv)); err != nil {
			t.Fatal(err)
		}
		return cond(dv, dotvirtv1alpha1.ConditionArgoWebhook)
	}

	got := pass()
	if got == nil || got.Status != metav1.ConditionUnknown || got.Reason != reasonRegistrationRefused {
		t.Fatalf("ArgoWebhook = %+v, want Unknown/%s", got, reasonRegistrationRefused)
	}
	for _, want := range []string{"https://argocd.example/api/webhook", "push only",
		dv.Namespace + "/" + install.ArgoWebhookSecretName, install.ArgoWebhookKey, "delivery log"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message lacks %q: %s", want, got.Message)
		}
	}
	if strings.Contains(got.Message, "hmac") {
		t.Errorf("message carries the secret: %s", got.Message)
	}
	asked := f.calls.Load()
	pass()
	if f.calls.Load() != asked {
		t.Errorf("a held pass made %d forge calls, want none", f.calls.Load()-asked)
	}

	setToken(t, c, dv, install.ArgoWebhookSecretName, "secret", "rotated")
	f.hooks.Store(http.StatusOK)
	if got := pass(); got.Status != metav1.ConditionTrue {
		t.Errorf("after the secret changed: %+v, want Registered", got)
	}

	f.hooks.Store(http.StatusBadGateway)
	setToken(t, c, dv, install.ArgoWebhookSecretName, "secret", "again")
	before := f.calls.Load()
	pass()
	pass()
	if got := cond(dv, dotvirtv1alpha1.ConditionArgoWebhook); got.Reason != "NotRegistered" {
		t.Errorf("outage: %+v, want NotRegistered", got)
	}
	if f.calls.Load()-before != 2 {
		t.Errorf("an outage made %d calls over two passes, want one each", f.calls.Load()-before)
	}
}
