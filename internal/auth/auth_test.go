package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/time/rate"
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

	if _, ok := parseCookieValue(value, []byte("other-secret")); ok {
		t.Error("accepted a cookie sealed with a different secret")
	}
	enc := strings.TrimPrefix(value, cookiePrefix)
	flipped := []byte(enc)
	i := len(flipped) / 2
	if flipped[i] == 'A' {
		flipped[i] = 'B'
	} else {
		flipped[i] = 'A'
	}
	if _, ok := parseCookieValue(cookiePrefix+string(flipped), secret); ok {
		t.Error("accepted tampered ciphertext")
	}
	if _, ok := parseCookieValue(cookiePrefix+"AAAA", secret); ok {
		t.Error("accepted a value shorter than the nonce")
	}
	if _, ok := parseCookieValue("garbage", secret); ok {
		t.Error("accepted a malformed cookie value")
	}
}

func TestCookieHidesToken(t *testing.T) {
	const token = "sha256~visible-token"
	a, b := cookieValue(token, secret), cookieValue(token, secret)
	if strings.Contains(a, token) || strings.Contains(a, base64.RawURLEncoding.EncodeToString([]byte(token))) {
		t.Error("sealed cookie carries the token in clear")
	}
	if a == b {
		t.Error("two seals of one token are identical: nonce is not random")
	}
}

// A typical projected SA JWT is ~1 KiB; the sealed value must stay well under
// the 4 KiB browser cookie cap.
func TestCookieSizeForJWT(t *testing.T) {
	jwt := strings.Repeat("x", 1400)
	if n := len(cookieName) + 1 + len(cookieValue(jwt, secret)); n > 4096 {
		t.Errorf("sealed cookie is %d bytes, over the 4 KiB cap", n)
	}
}

// Cookies minted before sealing (base64(token).hex(HMAC)) must read as logged
// out, never as an error.
func TestOldFormatCookieReadsLoggedOut(t *testing.T) {
	const token = "sha256~old"
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(token))
	old := base64.RawURLEncoding.EncodeToString([]byte(token)) + "." + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodGet, "/api/inventory", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: old})
	if _, ok := readCookie(req, secret); ok {
		t.Fatal("old-format cookie still opens")
	}
	a, _ := fakeAuth(map[string]authnv1.UserInfo{token: {Username: "alice"}})
	rec := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("old-format cookie reached the handler")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("old-format cookie: status %d, want 401", rec.Code)
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

// TestValidateThrottlesUnknownTokens pins the amplification bound: distinct
// unknown tokens beyond the review budget are refused without a TokenReview and
// without a rejection verdict, while a cached session keeps validating.
func TestValidateThrottlesUnknownTokens(t *testing.T) {
	a, calls := fakeAuth(map[string]authnv1.UserInfo{"good": {Username: "alice"}})
	a.reviews = rate.NewLimiter(0, 3) // a burst of 3 reviews, no refill
	ctx := context.Background()

	if _, err := a.Validate(ctx, "good"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for i := 0; i < 5; i++ {
		_, _ = a.Validate(ctx, fmt.Sprintf("spray-%d", i))
	}
	if *calls != 3 {
		t.Errorf("TokenReviews = %d, want the burst of 3 (1 session + 2 sprays)", *calls)
	}
	_, err := a.Validate(ctx, "spray-9")
	if !errors.Is(err, ErrThrottled) || errors.Is(err, ErrRejected) {
		t.Errorf("over budget: err = %v, want ErrThrottled and not a rejection", err)
	}
	if _, err := a.Validate(ctx, "good"); err != nil {
		t.Errorf("cached session must not queue behind the budget: %v", err)
	}
}

// A throttled sign-in is 429 with a retry hint: not 401 (nothing is known about
// the token) and not 503 (the apiserver is fine).
func TestLoginThrottledIs429(t *testing.T) {
	a, calls := fakeAuth(nil)
	a.reviews = rate.NewLimiter(0, 0)
	rec := httptest.NewRecorder()
	a.Login(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"x"}`)))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
	if *calls != 0 {
		t.Errorf("a throttled login still opened %d TokenReviews", *calls)
	}
}

// The API caps every body before Login runs; past the cap Login answers 413,
// and no TokenReview is spent on it.
func TestLoginOversizedBodyIs413(t *testing.T) {
	a, calls := fakeAuth(nil)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"`+strings.Repeat("x", 64)+`"}`))
	r.Body = http.MaxBytesReader(rec, r.Body, 16)
	a.Login(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
	if *calls != 0 {
		t.Errorf("an oversized login opened %d TokenReviews", *calls)
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

	// No credential -> 401, next not called.
	req := httptest.NewRequest("GET", "/api/inventory", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated request: status %d, want 401", rec.Code)
	}
	if sawIdentity {
		t.Error("next handler ran for an unauthenticated request")
	}

	// Bearer header -> 200, identity injected.
	req = httptest.NewRequest("GET", "/api/inventory", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !sawIdentity || seen.Username != "alice" {
		t.Errorf("expected injected identity for alice, got %+v (saw=%v)", seen, sawIdentity)
	}

	// Open path -> passes through without a credential.
	sawIdentity = false
	req = httptest.NewRequest("GET", "/api/healthz", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Error("health endpoint should bypass auth")
	}

	// Rejected token -> 401 (definitive).
	req = httptest.NewRequest("GET", "/api/inventory", nil)
	req.Header.Set("Authorization", "Bearer bad")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("rejected token: status %d, want 401", rec.Code)
	}
}

// TestMiddlewareTransientErrorIs503 ensures an API-server failure to validate is a
// 503, not a 401 - so a blip doesn't sign valid users out.
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
