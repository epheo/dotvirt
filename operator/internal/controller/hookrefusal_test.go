package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

// hookForge answers the org's hooks, the org lookup and the user lookup with
// the given statuses, a refusal carrying the forge's own reason, and counts
// every call.
type hookForge struct {
	hooks, org, user atomic.Int32
	calls            atomic.Int32
}

func (f *hookForge) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		switch r.URL.Path {
		case "/api/v1/orgs/dotvirt/hooks":
			if s := int(f.hooks.Load()); s != http.StatusOK {
				w.WriteHeader(s)
				_, _ = w.Write([]byte(`{"message":"Must be an organization owner"}`))
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
		case "/api/v1/users/dotvirt":
			w.WriteHeader(int(f.user.Load()))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// leakToken is the forge credential in these tests: distinctive, so a message
// carrying it is caught.
const leakToken = "tok-q8x2zv"

// hookSetup builds a managed-forge install whose forge is f, with the forge
// credential and the Argo webhook secret in place, and a settable clock.
func hookSetup(t *testing.T, f *hookForge) (*DotvirtReconciler, client.Client, *dotvirtv1alpha1.Dotvirt, *time.Time) {
	t.Helper()
	srv := f.serve(t)
	dv := managedForgeCR()
	dv.UID = "install-uid"
	dv.Spec.ArgoCD.ServerURL = "https://argocd.example"
	c := testBuilder(t).WithObjects(dv,
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: install.ForgeSecretName(dv), Namespace: dv.Namespace},
			Data:       map[string][]byte{"url": []byte(dv.Spec.Forge.URL), "token": []byte(leakToken)},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: install.ArgoWebhookSecretName, Namespace: dv.Namespace},
			Data:       map[string][]byte{"secret": []byte("hmac-w4k9")},
		},
	).Build()
	r := newReconciler(c, depsOK)
	r.forgeAPIBase = func(*dotvirtv1alpha1.Dotvirt) string { return srv.URL }
	clock := time.Unix(0, 0)
	r.now = func() time.Time { return clock }
	return r, c, dv, &clock
}

func setSecretKey(t *testing.T, c client.Client, dv *dotvirtv1alpha1.Dotvirt, name, key, value string) {
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

func dotvirtWebhookPass(t *testing.T, r *DotvirtReconciler, dv *dotvirtv1alpha1.Dotvirt) *metav1.Condition {
	t.Helper()
	if _, err := r.reconcileDotvirtWebhook(context.Background(), dv, nil); err != nil {
		t.Fatal(err)
	}
	return cond(dv, dotvirtv1alpha1.ConditionDotvirtWebhook)
}

func argoWebhookPassAt(t *testing.T, r *DotvirtReconciler, dv *dotvirtv1alpha1.Dotvirt) *metav1.Condition {
	t.Helper()
	if _, err := r.reconcileArgoWebhook(context.Background(), dv, phaseCtx(r, dv)); err != nil {
		t.Fatal(err)
	}
	return cond(dv, dotvirtv1alpha1.ConditionArgoWebhook)
}

func lacks(t *testing.T, msg string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	for _, secret := range []string{leakToken, "hmac-w4k9"} {
		if strings.Contains(msg, secret) {
			t.Errorf("message carries a secret: %s", msg)
		}
	}
}

// A forge refusing the operator's account the org's webhooks gets the
// forge's own reason and the hook to create by hand. It is held: asked again
// after an hour (a fix made in the forge), or at once when the credential
// changes.
func TestDotvirtWebhookRefusalIsGuidedAndHeld(t *testing.T) {
	f := &hookForge{}
	f.hooks.Store(http.StatusForbidden)
	r, c, dv, clock := hookSetup(t, f)

	got := dotvirtWebhookPass(t, r, dv)
	if got == nil || got.Status != metav1.ConditionUnknown || got.Reason != reasonRegistrationRefused {
		t.Fatalf("DotvirtWebhook = %+v, want Unknown/%s", got, reasonRegistrationRefused)
	}
	lacks(t, got.Message, "Must be an organization owner", "owner of the org", "/api/webhooks/forge", "push and pull_request",
		"application/json", dv.Namespace+"/"+install.WebhookSecretName, "oc extract secret/"+install.WebhookSecretName,
		"Forgejo (Gitea)", "system hook", "repos created later", "delivery log",
		"Rechecked hourly, and at once when spec.forge or the forge credential changes, or the operator restarts")

	asked := f.calls.Load()
	for range 5 {
		*clock = clock.Add(10 * time.Minute)
		dotvirtWebhookPass(t, r, dv)
	}
	if f.calls.Load() != asked {
		t.Errorf("held passes made %d forge calls, want none", f.calls.Load()-asked)
	}

	f.hooks.Store(http.StatusOK) // an admin made the account an owner; the credential is unchanged
	*clock = clock.Add(10 * time.Minute)
	if got := dotvirtWebhookPass(t, r, dv); got.Reason != "NotRegistered" {
		t.Errorf("an hour later: %+v, want the forge asked again (NotRegistered)", got)
	}

	f.hooks.Store(http.StatusForbidden)
	dotvirtWebhookPass(t, r, dv)
	setSecretKey(t, c, dv, install.ForgeSecretName(dv), "token", leakToken+"-b")
	f.hooks.Store(http.StatusOK)
	if got := dotvirtWebhookPass(t, r, dv); got.Reason != "NotRegistered" {
		t.Errorf("after a credential change: %+v, want the forge asked again", got)
	}
}

// A rejected token is the credential's problem, not a missing permission: it
// names the token to replace, and is held on that credential.
func TestDotvirtWebhookRejectedCredential(t *testing.T) {
	f := &hookForge{}
	f.hooks.Store(http.StatusUnauthorized)
	r, _, dv, _ := hookSetup(t, f)
	got := dotvirtWebhookPass(t, r, dv)
	if got == nil || got.Reason != reasonCredentialRejected {
		t.Fatalf("DotvirtWebhook = %+v, want %s", got, reasonCredentialRejected)
	}
	lacks(t, got.Message, "Secret "+dv.Namespace+"/"+install.ForgeSecretName(dv)+" key token", "replace the token")
	if strings.Contains(got.Message, "owner of the org") {
		t.Errorf("a rejected token reads as a permission: %s", got.Message)
	}
	asked := f.calls.Load()
	dotvirtWebhookPass(t, r, dv)
	if f.calls.Load() != asked {
		t.Error("a held rejection asked the forge again")
	}
}

// An outage is not a refusal: every pass asks again.
func TestDotvirtWebhookOutageRetries(t *testing.T) {
	f := &hookForge{}
	f.hooks.Store(http.StatusBadGateway)
	r, _, dv, _ := hookSetup(t, f)
	for range 3 {
		dotvirtWebhookPass(t, r, dv)
	}
	if got := cond(dv, dotvirtv1alpha1.ConditionDotvirtWebhook); got == nil || got.Reason != "Unobservable" {
		t.Errorf("DotvirtWebhook = %+v, want Unobservable", got)
	}
	if f.calls.Load() != 3 {
		t.Errorf("%d forge calls over three passes, want one each", f.calls.Load())
	}
}

// A 404 on org hooks asks, once, what the owner is: an org hiding its hooks
// (refused), a user account (per-repo hooks), or nobody (a wrong spec).
func TestWebhookOwnerKinds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		org, user     int32
		reason        string
		dotvirt, argo []string
	}{
		{"an org hiding its hooks", http.StatusOK, http.StatusNotFound, reasonRegistrationRefused,
			[]string{"HTTP 404"}, []string{"HTTP 404"}},
		{"a user account", http.StatusNotFound, http.StatusOK, reasonPerRepoHooks,
			[]string{"user account", "each project repo", "does not observe"},
			[]string{"user account", "each project repo", "system hook", "push events only", install.ArgoWebhookSecretName,
				"oc extract secret/" + install.ArgoWebhookSecretName, install.ArgoWebhookKey}},
		{"no such owner", http.StatusNotFound, http.StatusNotFound, reasonOwnerNotFound,
			[]string{"spec.forge.platformRepo", "spec.forge.url"}, []string{"spec.forge.platformRepo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &hookForge{}
			f.hooks.Store(http.StatusNotFound)
			f.org.Store(tc.org)
			f.user.Store(tc.user)
			r, _, dv, _ := hookSetup(t, f)
			got := dotvirtWebhookPass(t, r, dv)
			if got.Reason != tc.reason {
				t.Fatalf("DotvirtWebhook = %+v, want %s", got, tc.reason)
			}
			lacks(t, got.Message, tc.dotvirt...)
			asked := f.calls.Load()
			dotvirtWebhookPass(t, r, dv)
			if f.calls.Load() != asked {
				t.Error("a held owner answer asked the forge again")
			}
			if got := argoWebhookPassAt(t, r, dv); got.Reason != tc.reason {
				t.Errorf("ArgoWebhook = %+v, want %s", got, tc.reason)
			} else {
				lacks(t, got.Message, tc.argo...)
			}
		})
	}
}

// The ArgoCD hook: a refusal names the push-only hook and both places its
// secret lives, and is held until the secret, the credential, the ArgoCD URL
// or namespace changes, or an hour passes; an outage keeps retrying.
func TestArgoWebhookRefusalIsGuidedAndHeld(t *testing.T) {
	f := &hookForge{}
	f.hooks.Store(http.StatusForbidden)
	r, c, dv, clock := hookSetup(t, f)

	got := argoWebhookPassAt(t, r, dv)
	if got == nil || got.Status != metav1.ConditionUnknown || got.Reason != reasonRegistrationRefused {
		t.Fatalf("ArgoWebhook = %+v, want Unknown/%s", got, reasonRegistrationRefused)
	}
	lacks(t, got.Message, "https://argocd.example/api/webhook", "push only", "Must be an organization owner",
		dv.Namespace+"/"+install.ArgoWebhookSecretName, install.ArgoWebhookKey, "delivery log",
		"the forge credential, the webhook secret or the ArgoCD URL or namespace changes")
	asked := f.calls.Load()
	*clock = clock.Add(30 * time.Minute)
	argoWebhookPassAt(t, r, dv)
	if f.calls.Load() != asked {
		t.Errorf("a held pass made %d forge calls, want none", f.calls.Load()-asked)
	}

	setSecretKey(t, c, dv, install.ArgoWebhookSecretName, "secret", "hmac-w4k9-rotated")
	f.hooks.Store(http.StatusOK)
	if got := argoWebhookPassAt(t, r, dv); got.Status != metav1.ConditionTrue {
		t.Errorf("after the secret changed: %+v, want Registered", got)
	}

	f.hooks.Store(http.StatusBadGateway)
	setSecretKey(t, c, dv, install.ArgoWebhookSecretName, "secret", "hmac-w4k9-again")
	before := f.calls.Load()
	argoWebhookPassAt(t, r, dv)
	argoWebhookPassAt(t, r, dv)
	if got := cond(dv, dotvirtv1alpha1.ConditionArgoWebhook); got.Reason != "NotRegistered" {
		t.Errorf("outage: %+v, want NotRegistered", got)
	}
	if f.calls.Load()-before != 2 {
		t.Errorf("an outage made %d calls over two passes, want one each", f.calls.Load()-before)
	}
}

// A 404 on org hooks followed by an outage on the owner lookup is not a
// refusal: nothing is held, and the next pass asks again.
func TestOwnerLookupOutageIsNotHeld(t *testing.T) {
	f := &hookForge{}
	f.hooks.Store(http.StatusNotFound)
	f.org.Store(http.StatusBadGateway)
	r, _, dv, _ := hookSetup(t, f)
	if got := dotvirtWebhookPass(t, r, dv); got.Reason != "Unobservable" {
		t.Errorf("DotvirtWebhook = %+v, want Unobservable", got)
	}
	asked := f.calls.Load()
	dotvirtWebhookPass(t, r, dv)
	if f.calls.Load() == asked {
		t.Error("an outage on the owner lookup was held")
	}
	if got := argoWebhookPassAt(t, r, dv); got.Reason != "NotRegistered" {
		t.Errorf("ArgoWebhook = %+v, want NotRegistered", got)
	}
}
