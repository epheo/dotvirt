package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeIssuer is an OIDC issuer with Kanidm's refresh semantics: a refresh
// token is spent once, and spending one twice revokes the whole grant.
type fakeIssuer struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	challenge string          // the last authorize redirect's PKCE challenge
	nonce     string          // and its nonce
	live      map[string]bool // refresh tokens not spent yet
	minted    map[string]int  // ID tokens by their n, which the fake TokenReview accepts
	rejectAt  int             // non-zero: the TokenReview rejects tokens minted from this n on
	sub       string          // the subject tokens name from now on
	hold      chan struct{}   // non-nil: a refresh waits on it, after telling held
	held      chan struct{}
	revoked   []string
	n         int
	ttl       time.Duration
	refreshes atomic.Int32
	// refreshStatus, when set, answers every refresh grant with it and refreshErr.
	refreshStatus int
	refreshErr    string
	issuer        string // what discovery names; the server's URL unless set
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	f := &fakeIssuer{t: t, live: map[string]bool{}, minted: map[string]int{}, ttl: 15 * time.Minute, sub: "alice"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth2/openid/dotvirt/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := f.srv.URL + "/oauth2/openid/dotvirt"
		f.mu.Lock()
		issuer := f.issuer
		f.mu.Unlock()
		if issuer == "" {
			issuer = base
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": f.srv.URL + "/ui/oauth2",
			"token_endpoint":         f.srv.URL + "/oauth2/token",
			"revocation_endpoint":    f.srv.URL + "/oauth2/token/revoke",
		})
	})
	mux.HandleFunc("POST /oauth2/token", f.token)
	mux.HandleFunc("POST /oauth2/token/revoke", func(w http.ResponseWriter, r *http.Request) {
		if id, secret, _ := r.BasicAuth(); id != "dotvirt" || secret != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.revoked = append(f.revoked, r.PostFormValue("token"))
		delete(f.live, r.PostFormValue("token"))
		f.mu.Unlock()
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIssuer) issuerURL() string { return f.srv.URL + "/oauth2/openid/dotvirt" }

func tokenError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func (f *fakeIssuer) token(w http.ResponseWriter, r *http.Request) {
	if id, secret, _ := r.BasicAuth(); id != "dotvirt" || secret != "s3cret" {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	f.mu.Lock()
	hold, held := f.hold, f.held
	f.mu.Unlock()
	if hold != nil && r.PostFormValue("grant_type") == "refresh_token" {
		held <- struct{}{}
		<-hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var nonce string
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
		if r.PostFormValue("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
			tokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		nonce = f.nonce
	case "refresh_token":
		f.refreshes.Add(1)
		if f.refreshStatus != 0 {
			tokenError(w, f.refreshStatus, f.refreshErr)
			return
		}
		rt := r.PostFormValue("refresh_token")
		if !f.live[rt] {
			clear(f.live) // reuse revokes the whole grant
			tokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		delete(f.live, rt)
	default:
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	f.n++
	claims, _ := json.Marshal(map[string]any{"iss": f.issuerURL(), "sub": f.sub, "exp": time.Now().Add(f.ttl).Unix(), "nonce": nonce, "n": f.n})
	id := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	rt := fmt.Sprintf("r%d", f.n)
	f.minted[id], f.live[rt] = f.n, true
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": fmt.Sprintf("a%d", f.n), "token_type": "Bearer", "expires_in": int(f.ttl.Seconds()),
		"id_token": id, "refresh_token": rt,
	})
}

func (f *fakeIssuer) mintedToken(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.minted[token] != 0
}

// accepted is the fake TokenReview's verdict on token.
func (f *fakeIssuer) accepted(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.minted[token]
	return n != 0 && (f.rejectAt == 0 || n < f.rejectAt)
}

// oidcRig is the flow over a fake issuer, the middleware over one route that
// records the identity it injected, and logout.
type oidcRig struct {
	f    *fakeIssuer
	a    *Authenticator
	o    *OIDC
	h    http.Handler
	mu   sync.Mutex
	seen []Identity
}

func newOIDCRig(t *testing.T) *oidcRig {
	t.Helper()
	f := newFakeIssuer(t)
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		tr := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview)
		out := tr.DeepCopy()
		out.Status = authnv1.TokenReviewStatus{Authenticated: false, Error: "bad token"}
		if f.accepted(tr.Spec.Token) {
			out.Status = authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: "uw:alice"}}
		}
		return true, out, nil
	})
	a := New(kube, secret)
	o := NewOIDC(OIDCConfig{
		Issuer: f.issuerURL(), ClientID: "dotvirt", ClientSecret: "s3cret",
		RedirectURL: "https://dotvirt.example/api/auth/callback",
		Scopes:      []string{"profile", "openid", "groups_name"},
	}, a)
	rig := &oidcRig{f: f, a: a, o: o}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/inventory", func(_ http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		rig.mu.Lock()
		rig.seen = append(rig.seen, id)
		rig.mu.Unlock()
	})
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		id, ended := a.Logout(w, r)
		if ended {
			if err := o.Revoke(r.Context(), id); err != nil {
				t.Errorf("revoke: %v", err)
			}
		}
	})
	rig.h = a.Middleware(mux)
	return rig
}

// signIn runs the redirect and the callback, and returns the session cookie.
func (rig *oidcRig) signIn(t *testing.T) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	rig.o.LoginRedirect(rec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("redirect = %d %s", rec.Code, rec.Body)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("scope") != "openid profile groups_name" || q.Get("nonce") == "" {
		t.Fatalf("authorize request = %s", loc)
	}
	rig.f.mu.Lock()
	rig.f.challenge, rig.f.nonce = q.Get("code_challenge"), q.Get("nonce")
	rig.f.mu.Unlock()
	return rig.callback(t, rec.Result().Cookies(), q.Get("state"))
}

func (rig *oidcRig) callback(t *testing.T, cookies []*http.Cookie, state string) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=the-code&state="+url.QueryEscape(state), nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	rig.o.Callback(rec, req)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/" {
		return nil
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && c.MaxAge >= 0 {
			return c
		}
	}
	t.Fatal("the callback set no session cookie")
	return nil
}

func (rig *oidcRig) send(method, path string, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.AddCookie(c)
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, r)
	return rec
}

func (rig *oidcRig) last() Identity {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	return rig.seen[len(rig.seen)-1]
}

// dueNow makes the one session's renewal due, past any failed one's pause.
func (rig *oidcRig) dueNow() {
	rig.a.oidc.mu.Lock()
	defer rig.a.oidc.mu.Unlock()
	for _, s := range rig.a.oidc.m {
		s.mu.Lock()
		s.renewAt, s.retryAt = time.Now().Add(-time.Second), time.Time{}
		s.mu.Unlock()
	}
}

// sessionKey is the one session's key.
func (rig *oidcRig) sessionKey(t *testing.T) string {
	t.Helper()
	rig.a.oidc.mu.Lock()
	defer rig.a.oidc.mu.Unlock()
	if len(rig.a.oidc.m) != 1 {
		t.Fatalf("sessions = %d, want 1", len(rig.a.oidc.m))
	}
	for k := range rig.a.oidc.m {
		return k
	}
	return ""
}

// The cookie carries a handle, never a token; the bearer the middleware
// presents is the issuer's ID token, under the session's key.
func TestOIDCSignIn(t *testing.T) {
	rig := newOIDCRig(t)
	c := rig.signIn(t)
	if c == nil {
		t.Fatal("sign-in failed")
	}
	if rec := rig.send(http.MethodGet, "/api/inventory", c); rec.Code != http.StatusOK {
		t.Fatalf("request = %d %s", rec.Code, rec.Body)
	}
	id := rig.last()
	if id.Username != "uw:alice" || !id.SSO || !strings.HasPrefix(id.Session, oidcKeyPrefix) || id.Key() != id.Session {
		t.Fatalf("identity = %+v", id)
	}
	if !rig.f.mintedToken(id.Token) {
		t.Fatalf("bearer %q is not the issuer's ID token", id.Token)
	}
	s, _ := parseCookieValue(c.Value, rig.a.sealer)
	if !s.oidc || !s.sso || strings.Contains(c.Value, id.Token) || s.token == id.Token {
		t.Fatalf("cookie = %+v", s)
	}
	if !rig.a.SessionLive(id.Key(), id.Issued) {
		t.Fatal("a signed-in OIDC session must read live")
	}
}

// The issuer accepts a refresh token once: a burst of a session's requests
// past its renewal time spends it once, and all of them carry the new token.
func TestOIDCRenewsOncePerSession(t *testing.T) {
	rig := newOIDCRig(t)
	c := rig.signIn(t)
	rig.send(http.MethodGet, "/api/inventory", c)
	before := rig.last().Token
	rig.dueNow()

	var wg sync.WaitGroup
	codes := make(chan int, 20)
	for range 20 {
		wg.Go(func() { codes <- rig.send(http.MethodGet, "/api/inventory", c).Code })
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusOK {
			t.Fatalf("a request during the renewal = %d", code)
		}
	}
	if n := rig.f.refreshes.Load(); n != 1 {
		t.Fatalf("refresh grants = %d, want 1", n)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	for _, id := range rig.seen[1:] {
		if id.Token == before || !rig.f.mintedToken(id.Token) {
			t.Fatalf("a request after the renewal carried %q", id.Token)
		}
	}
}

// A stream re-checks its session by key and builds with its current token,
// not the one it opened with.
func TestOIDCRevalidateCarriesTheCurrentToken(t *testing.T) {
	rig := newOIDCRig(t)
	c := rig.signIn(t)
	rig.send(http.MethodGet, "/api/inventory", c)
	opened := rig.last()
	rig.dueNow()
	cur, err := rig.a.Revalidate(context.Background(), opened)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Token == opened.Token || cur.Session != opened.Session || cur.Issued != opened.Issued || !cur.SSO {
		t.Fatalf("revalidated = %+v, opened = %+v", cur, opened)
	}
	if err := rig.a.Recheck(context.Background(), opened); err != nil {
		t.Fatalf("recheck of a renewed session = %v", err)
	}
}

// A refused grant ends the session: the cookie is cleared and the server
// forgets the session.
func TestOIDCRefusedGrantEndsTheSession(t *testing.T) {
	rig := newOIDCRig(t)
	c := rig.signIn(t)
	rig.send(http.MethodGet, "/api/inventory", c)
	key := rig.last().Session
	rig.f.mu.Lock()
	rig.f.refreshStatus, rig.f.refreshErr = http.StatusBadRequest, "invalid_grant"
	rig.f.mu.Unlock()
	rig.dueNow()
	rec := rig.send(http.MethodGet, "/api/inventory", c)
	if rec.Code != http.StatusUnauthorized || !clears(rec) {
		t.Fatalf("after a refused grant = %d, clears = %v", rec.Code, clears(rec))
	}
	if rig.a.SessionLive(key, time.Now()) {
		t.Fatal("the session outlived its grant")
	}
}

// A renewal that cannot run serves the token while it lives, and is retried
// after a pause, not by every request.
func TestOIDCRenewalOutageKeepsTheLiveToken(t *testing.T) {
	rig := newOIDCRig(t)
	c := rig.signIn(t)
	rig.f.mu.Lock()
	rig.f.refreshStatus, rig.f.refreshErr = http.StatusServiceUnavailable, "temporarily_unavailable"
	rig.f.mu.Unlock()
	rig.dueNow()
	for range 3 {
		if rec := rig.send(http.MethodGet, "/api/inventory", c); rec.Code != http.StatusOK {
			t.Fatalf("during the outage = %d", rec.Code)
		}
	}
	if n := rig.f.refreshes.Load(); n != 1 {
		t.Fatalf("refresh grants during the outage = %d, want 1", n)
	}
}

// Logout ends the session alone and revokes its newest refresh token at the
// issuer.
func TestOIDCLogoutRevokes(t *testing.T) {
	rig := newOIDCRig(t)
	c := rig.signIn(t)
	rig.send(http.MethodGet, "/api/inventory", c)
	rig.dueNow()
	rig.send(http.MethodGet, "/api/inventory", c) // r1 spent, r2 held
	key := rig.last().Session
	if rec := rig.send(http.MethodPost, "/api/logout", c); rec.Code != http.StatusNoContent || !clears(rec) {
		t.Fatalf("logout = %d", rec.Code)
	}
	rig.f.mu.Lock()
	revoked := append([]string(nil), rig.f.revoked...)
	rig.f.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != "r2" {
		t.Fatalf("revoked = %v, want the newest refresh token", revoked)
	}
	if rig.a.SessionLive(key, time.Now()) {
		t.Fatal("logout left the session live")
	}
	if rec := rig.send(http.MethodGet, "/api/inventory", c); rec.Code != http.StatusUnauthorized || !clears(rec) {
		t.Fatalf("the signed-out cookie = %d", rec.Code)
	}
}

// A cookie whose session the server does not hold (a restart forgot it) reads
// as signed out, without a TokenReview.
func TestOIDCUnknownSessionIsSignedOut(t *testing.T) {
	rig := newOIDCRig(t)
	c := &http.Cookie{Name: cookieName, Value: cookieValue(session{token: "forgotten", issued: time.Now(), sso: true, oidc: true}, rig.a.sealer)}
	if rec := rig.send(http.MethodGet, "/api/inventory", c); rec.Code != http.StatusUnauthorized || !clears(rec) {
		t.Fatalf("unknown session = %d", rec.Code)
	}
}

// The callback starts a session only for the round-trip it began: a token
// carrying another nonce is refused, and so is a code without its verifier.
func TestOIDCCallbackChecksTheRoundTrip(t *testing.T) {
	rig := newOIDCRig(t)
	rec := httptest.NewRecorder()
	rig.o.LoginRedirect(rec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc", nil))
	q := must(url.Parse(rec.Header().Get("Location"))).Query()

	rig.f.mu.Lock()
	rig.f.challenge, rig.f.nonce = q.Get("code_challenge"), "another-nonce"
	rig.f.mu.Unlock()
	if rig.callback(t, rec.Result().Cookies(), q.Get("state")) != nil {
		t.Fatal("a token with another nonce started a session")
	}

	rig.f.mu.Lock()
	rig.f.challenge, rig.f.nonce = "another-challenge", q.Get("nonce")
	rig.f.mu.Unlock()
	if rig.callback(t, rec.Result().Cookies(), q.Get("state")) != nil {
		t.Fatal("a code exchanged without its verifier started a session")
	}
	if len(rig.a.oidc.m) != 0 {
		t.Fatalf("sessions = %d", len(rig.a.oidc.m))
	}
}

// The state is the login-CSRF guard: a callback carrying another state, or
// none of the cookie that seals it, starts no session, though its code and
// verifier would exchange.
func TestOIDCCallbackChecksTheState(t *testing.T) {
	rig := newOIDCRig(t)
	rec := httptest.NewRecorder()
	rig.o.LoginRedirect(rec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc", nil))
	q := must(url.Parse(rec.Header().Get("Location"))).Query()
	rig.f.mu.Lock()
	rig.f.challenge, rig.f.nonce = q.Get("code_challenge"), q.Get("nonce")
	rig.f.mu.Unlock()
	if rig.callback(t, rec.Result().Cookies(), randomToken()) != nil {
		t.Fatal("a callback with another state started a session")
	}
	if rig.callback(t, nil, q.Get("state")) != nil {
		t.Fatal("a callback without the state cookie started a session")
	}
	if rig.callback(t, rec.Result().Cookies(), q.Get("state")) == nil {
		t.Fatal("the round-trip's own state must sign in")
	}
}

// A renewal must name the session's subject (OIDC Core 12.2): what is cached
// under the session key holds for that subject alone.
func TestOIDCRenewalKeepsTheSubject(t *testing.T) {
	rig := newOIDCRig(t)
	c := rig.signIn(t)
	key := rig.sessionKey(t)
	rig.f.mu.Lock()
	rig.f.sub = "mallory"
	rig.f.mu.Unlock()
	rig.dueNow()
	if rec := rig.send(http.MethodGet, "/api/inventory", c); rec.Code != http.StatusUnauthorized || !clears(rec) {
		t.Fatalf("a renewal naming another subject = %d", rec.Code)
	}
	if rig.a.SessionLive(key, time.Now()) {
		t.Fatal("the session outlived a change of subject")
	}
}

// A session past its lifetime is over for its streams too, which reach the
// store through SessionLive, not through the cookie's issue time.
func TestOIDCSessionLifetime(t *testing.T) {
	s := newOIDCSessions()
	s.renew = func(context.Context, string) (oidcTokens, error) {
		t.Fatal("an expired session renewed")
		return oidcTokens{}, nil
	}
	old := s.start(oidcTokens{id: "x", refresh: "r", expiry: time.Now().Add(time.Hour)}, time.Now().Add(-sessionMaxAge-time.Second))
	key := oidcSessionKey(old)
	if s.live(key) {
		t.Fatal("a session past its lifetime reads live")
	}
	if _, err := s.current(context.Background(), key); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("current = %v, want ErrSessionEnded", err)
	}
}

// A renewal is due once three quarters of the token's life has passed, before
// it expires; not before.
func TestOIDCRenewsAtThreeQuarters(t *testing.T) {
	s := newOIDCSessions()
	var renewed int
	s.renew = func(context.Context, string) (oidcTokens, error) {
		renewed++
		return oidcTokens{id: "next", refresh: "r2", expiry: time.Now().Add(40 * time.Second)}, nil
	}
	now := time.Now()
	early := oidcSessionKey(s.start(oidcTokens{id: "early", refresh: "r", expiry: now.Add(11 * time.Second)}, now.Add(-29*time.Second)))
	if got, _ := s.current(context.Background(), early); got != "early" || renewed != 0 {
		t.Fatalf("before three quarters: %q, renewals %d", got, renewed)
	}
	due := oidcSessionKey(s.start(oidcTokens{id: "due", refresh: "r", expiry: now.Add(9 * time.Second)}, now.Add(-31*time.Second)))
	if got, _ := s.current(context.Background(), due); got != "next" || renewed != 1 {
		t.Fatalf("past three quarters: %q, renewals %d", got, renewed)
	}
}

// A renewal outlives the request that started it: the issuer has spent the
// old refresh token, so the session must hold the new one, or its next
// renewal presents the spent one and the issuer revokes the grant.
func TestOIDCRenewalOutlivesItsRequest(t *testing.T) {
	rig := newOIDCRig(t)
	rig.signIn(t)
	key := rig.sessionKey(t)
	rig.f.mu.Lock()
	rig.f.hold, rig.f.held = make(chan struct{}), make(chan struct{})
	hold, held := rig.f.hold, rig.f.held
	rig.f.mu.Unlock()
	rig.dueNow()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = rig.a.oidc.current(ctx, key)
		close(done)
	}()
	<-held
	cancel()
	rig.f.mu.Lock()
	rig.f.hold = nil
	rig.f.mu.Unlock()
	close(hold)
	<-done

	rig.dueNow()
	if _, err := rig.a.oidc.current(context.Background(), key); err != nil {
		t.Fatalf("the renewal after an abandoned one = %v", err)
	}
	if n := rig.f.refreshes.Load(); n != 2 {
		t.Fatalf("refresh grants = %d, want 2", n)
	}
}

// A renewed token the cluster rejects ends its session: no later renewal
// makes it valid again.
func TestOIDCRejectedTokenEndsTheSession(t *testing.T) {
	rig := newOIDCRig(t)
	c := rig.signIn(t)
	key := rig.sessionKey(t)
	rig.f.mu.Lock()
	rig.f.rejectAt = rig.f.n + 1
	rig.f.mu.Unlock()
	rig.dueNow()
	if rec := rig.send(http.MethodGet, "/api/inventory", c); rec.Code != http.StatusUnauthorized || !clears(rec) {
		t.Fatalf("a rejected renewed token = %d", rec.Code)
	}
	if rig.a.SessionLive(key, time.Now()) {
		t.Fatal("the session outlived its rejected token")
	}
}

// An issuer naming itself other than configured would fail every sign-in at
// the apiserver: discovery refuses it before the redirect.
func TestOIDCDiscoveryChecksTheIssuer(t *testing.T) {
	rig := newOIDCRig(t)
	rig.f.mu.Lock()
	rig.f.issuer = rig.f.issuerURL() + "/"
	rig.f.mu.Unlock()
	rec := httptest.NewRecorder()
	rig.o.LoginRedirect(rec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("redirect with a mismatched issuer = %d", rec.Code)
	}
	if _, err := rig.o.discover(context.Background()); err == nil || !strings.Contains(err.Error(), "names itself") {
		t.Fatalf("discover = %v", err)
	}
}

// The store's cap signs the oldest session out, not the newest.
func TestOIDCSessionCap(t *testing.T) {
	s := newOIDCSessions()
	s.limit = 2
	now := time.Now()
	t0 := oidcTokens{id: "x", expiry: now.Add(time.Hour)}
	first := s.start(t0, now.Add(-2*time.Minute))
	second := s.start(t0, now.Add(-time.Minute))
	third := s.start(t0, now)
	if s.live(oidcSessionKey(first)) || !s.live(oidcSessionKey(second)) || !s.live(oidcSessionKey(third)) {
		t.Fatal("the cap must sign the oldest session out")
	}
	if _, err := s.current(context.Background(), oidcSessionKey(first)); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("evicted session = %v", err)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
