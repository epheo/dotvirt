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
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var (
	secret     = []byte("test-secret-key")
	testSealer = newSealer(secret)
)

func TestCookieRoundTrip(t *testing.T) {
	const token = "sha256~abc.def-ghi"
	for _, want := range []session{
		{token: token, issued: time.Unix(1700000000, 123)},
		{token: token, issued: time.Unix(1700000000, 123), sso: true},
	} {
		got, ok := parseCookieValue(cookieValue(want, testSealer), testSealer)
		if !ok {
			t.Fatal("parseCookieValue rejected a value it just signed")
		}
		if got.token != want.token || !got.issued.Equal(want.issued) || got.sso != want.sso {
			t.Errorf("round-trip changed the session: got %+v, want %+v", got, want)
		}
	}
}

func TestCookieTamperRejected(t *testing.T) {
	value := cookieValue(session{token: "real-token", issued: time.Now()}, testSealer)

	if _, ok := parseCookieValue(value, newSealer([]byte("other-secret"))); ok {
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
	if _, ok := parseCookieValue(cookiePrefix+string(flipped), testSealer); ok {
		t.Error("accepted tampered ciphertext")
	}
	if _, ok := parseCookieValue(cookiePrefix+"AAAA", testSealer); ok {
		t.Error("accepted a value shorter than the nonce")
	}
	if _, ok := parseCookieValue(legacyPrefix+strings.TrimPrefix(value, cookiePrefix), testSealer); ok {
		t.Error("accepted a sealed value under the other format's prefix")
	}
	if _, ok := parseCookieValue("garbage", testSealer); ok {
		t.Error("accepted a malformed cookie value")
	}
}

func TestCookieHidesToken(t *testing.T) {
	const token = "sha256~visible-token"
	s := session{token: token, issued: time.Now()}
	a, b := cookieValue(s, testSealer), cookieValue(s, testSealer)
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
	if n := len(cookieName) + 1 + len(cookieValue(session{token: jwt, issued: time.Now(), sso: true}, testSealer)); n > 4096 {
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
	if _, ok := readCookie(req, testSealer); ok {
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

// TestRecheck pins the session-end rule: a token the apiserver refused is
// re-reviewed despite the cached session, a rejection is cached and answered
// without another review, and a token that still validates is not rejected.
func TestRecheck(t *testing.T) {
	valid := map[string]authnv1.UserInfo{"good": {Username: "alice"}, "fine": {Username: "bob"}}
	a, calls := fakeAuth(valid)
	for _, tok := range []string{"good", "fine"} {
		if _, err := a.Validate(context.Background(), tok); err != nil {
			t.Fatalf("Validate(%s): %v", tok, err)
		}
	}
	delete(valid, "good")
	if err := a.Recheck(context.Background(), "good"); !errors.Is(err, ErrRejected) || *calls != 3 {
		t.Fatalf("dead token: err = %v, reviews = %d; want a fresh rejecting review", err, *calls)
	}
	if err := a.Recheck(context.Background(), "good"); !errors.Is(err, ErrRejected) || *calls != 3 {
		t.Fatalf("cached rejection: err = %v, reviews = %d; want no further review", err, *calls)
	}
	if err := a.Recheck(context.Background(), "fine"); err != nil || *calls != 4 {
		t.Fatalf("live token: err = %v, reviews = %d; want a fresh accepting review", err, *calls)
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

// A live session's re-check spends nothing from the sign-in budget, so
// streams re-checking cannot starve a sign-in or be refused into a pass, and
// concurrent re-checks of one token share one TokenReview.
func TestRevalidateOutsideTheBudget(t *testing.T) {
	a, calls := fakeAuth(map[string]authnv1.UserInfo{"good": {Username: "alice"}})
	a.reviews = rate.NewLimiter(0, 0) // no sign-in budget at all
	gate := make(chan struct{})
	kube := a.saKube.(*fake.Clientset)
	kube.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-gate
		return false, nil, nil
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := a.Revalidate(context.Background(), Identity{Token: "good"}); err != nil {
				t.Errorf("Revalidate: %v", err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond) // let every re-check join the review in flight
	close(gate)
	wg.Wait()
	if *calls != 1 {
		t.Errorf("TokenReviews = %d for 8 concurrent re-checks, want 1", *calls)
	}
	if _, err := a.Validate(context.Background(), "unknown"); !errors.Is(err, ErrThrottled) {
		t.Errorf("an unknown token past the budget: %v, want ErrThrottled", err)
	}
	if err := a.Revalidate(context.Background(), Identity{Token: "bad"}); !errors.Is(err, ErrRejected) {
		t.Errorf("a dead token re-checked: %v, want ErrRejected", err)
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
	// The branding the login screen wears is read before any session; a path
	// beneath it that serves nothing is not open.
	for path, open := range map[string]bool{"/api/branding": true, "/api/branding/theme.css": true, "/api/branding/logo": true, "/api/branding/other": false} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if (rec.Code != http.StatusUnauthorized) != open {
			t.Errorf("%s without a session: %d, want open %v", path, rec.Code, open)
		}
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
