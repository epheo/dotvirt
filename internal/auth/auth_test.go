package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var secret = []byte("test-secret-key")

func TestCookieRoundTrip(t *testing.T) {
	const token = "sha256~abc.def-ghi"
	value := cookieValue(token, secret)

	got, ok := parseCookieValue(value, secret)
	if !ok {
		t.Fatal("parseCookieValue rejected a value it just signed")
	}
	if got != token {
		t.Errorf("round-trip changed the token: got %q want %q", got, token)
	}
}

func TestCookieTamperRejected(t *testing.T) {
	value := cookieValue("real-token", secret)

	// Wrong secret: the MAC won't verify.
	if _, ok := parseCookieValue(value, []byte("other-secret")); ok {
		t.Error("accepted a cookie signed with a different secret")
	}
	// Mangled MAC.
	if _, ok := parseCookieValue(value+"00", secret); ok {
		t.Error("accepted a cookie with a tampered MAC")
	}
	// No separator.
	if _, ok := parseCookieValue("garbage", secret); ok {
		t.Error("accepted a malformed cookie value")
	}
}

// fakeAuth wires an Authenticator over a fake kube client whose TokenReview
// reactor authenticates exactly the tokens in valid.
func fakeAuth(valid map[string]authnv1.UserInfo) (*Authenticator, *int) {
	kube := fake.NewSimpleClientset()
	calls := 0
	kube.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		tr := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview)
		out := tr.DeepCopy()
		if user, ok := valid[tr.Spec.Token]; ok {
			out.Status = authnv1.TokenReviewStatus{Authenticated: true, User: user}
		} else {
			out.Status = authnv1.TokenReviewStatus{Authenticated: false, Error: "bad token"}
		}
		return true, out, nil
	})
	return New(kube, secret), &calls
}

func TestValidateAuthenticated(t *testing.T) {
	a, _ := fakeAuth(map[string]authnv1.UserInfo{
		"good": {Username: "alice", Groups: []string{"dev", "system:authenticated"}},
	})

	id, err := a.Validate(context.Background(), "good")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if id.Username != "alice" {
		t.Errorf("username = %q, want alice", id.Username)
	}
	if id.Token != "good" {
		t.Errorf("identity should carry the raw token, got %q", id.Token)
	}
	if len(id.Groups) != 2 {
		t.Errorf("groups = %v, want 2", id.Groups)
	}
}

func TestValidateRejected(t *testing.T) {
	a, _ := fakeAuth(nil)
	if _, err := a.Validate(context.Background(), "nope"); err == nil {
		t.Error("expected rejection for an unknown token")
	}
	if _, err := a.Validate(context.Background(), ""); err == nil {
		t.Error("expected rejection for an empty token")
	}
}

func TestValidateCaches(t *testing.T) {
	a, calls := fakeAuth(map[string]authnv1.UserInfo{"good": {Username: "alice"}})

	for i := 0; i < 3; i++ {
		if _, err := a.Validate(context.Background(), "good"); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	}
	if *calls != 1 {
		t.Errorf("expected 1 TokenReview (cached after first), got %d", *calls)
	}

	// Negative results are cached too.
	for i := 0; i < 3; i++ {
		_, _ = a.Validate(context.Background(), "bad")
	}
	if *calls != 2 {
		t.Errorf("expected 1 more TokenReview for the bad token, got %d total", *calls)
	}
}

func TestMiddlewareInjectsIdentity(t *testing.T) {
	a, _ := fakeAuth(map[string]authnv1.UserInfo{"good": {Username: "alice"}})

	var seen Identity
	var sawIdentity bool
	next := func(w http.ResponseWriter, r *http.Request) {
		seen, sawIdentity = FromContext(r.Context())
	}
	h := a.Middleware(http.HandlerFunc(next))

	// No credential → 401, next not called.
	req := httptest.NewRequest("GET", "/api/inventory", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated request: status %d, want 401", rec.Code)
	}
	if sawIdentity {
		t.Error("next handler ran for an unauthenticated request")
	}

	// Bearer header → 200, identity injected.
	req = httptest.NewRequest("GET", "/api/inventory", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !sawIdentity || seen.Username != "alice" {
		t.Errorf("expected injected identity for alice, got %+v (saw=%v)", seen, sawIdentity)
	}

	// Open path → passes through without a credential.
	sawIdentity = false
	req = httptest.NewRequest("GET", "/api/healthz", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Error("health endpoint should bypass auth")
	}

	// Rejected token → 401 (definitive).
	req = httptest.NewRequest("GET", "/api/inventory", nil)
	req.Header.Set("Authorization", "Bearer bad")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("rejected token: status %d, want 401", rec.Code)
	}
}

// TestMiddlewareTransientErrorIs503 ensures an API-server failure to validate is a
// 503, not a 401 — so a blip doesn't sign valid users out.
func TestMiddlewareTransientErrorIs503(t *testing.T) {
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	a := New(kube, secret)

	h := a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest("GET", "/api/inventory", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("transient TokenReview failure: status %d, want 503", rec.Code)
	}
}
