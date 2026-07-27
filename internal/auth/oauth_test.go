package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
)

// fakeOAuthAt wires the flow over a stub oauth server and a pre-seeded
// discovery document (the fake clientset cannot serve the well-known path).
func fakeOAuthAt(t *testing.T, a *Authenticator, h http.HandlerFunc) *OAuth {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return &OAuth{
		cfg: OAuthConfig{
			ClientID: "dotvirt", ClientSecret: "s3cret",
			RedirectURL: "https://dotvirt.example.com/api/auth/callback",
		},
		auth:   a,
		client: ts.Client(),
		meta: &oauthMeta{
			AuthorizationEndpoint: ts.URL + "/oauth/authorize",
			TokenEndpoint:         ts.URL + "/oauth/token",
		},
	}
}

// fakeOAuth returns accessToken for any code, mirroring osin's happy path.
func fakeOAuth(t *testing.T, accessToken string, a *Authenticator) *OAuth {
	t.Helper()
	return fakeOAuthAt(t, a, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": accessToken, "token_type": "Bearer",
		})
	})
}

// oauthErrorStub answers the authorize probe OK and every token exchange with
// the given RFC 6749 error code.
func oauthErrorStub(code string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/authorize":
			w.WriteHeader(http.StatusOK)
		case "/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
		default:
			http.NotFound(w, r)
		}
	}
}

// stateFromRedirect pulls the state param and the signed state cookie out of a
// LoginRedirect response.
func stateFromRedirect(t *testing.T, o *OAuth) (state string, cookie *http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	o.LoginRedirect(rec, httptest.NewRequest(http.MethodGet, "/api/auth/openshift", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("LoginRedirect status = %d, want 302", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Query().Get("client_id"); got != "dotvirt" {
		t.Errorf("client_id = %q", got)
	}
	if got := loc.Query().Get("scope"); got != "user:full" {
		t.Errorf("scope = %q, want user:full (the token must act as the user)", got)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == stateCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no state cookie set")
	}
	return loc.Query().Get("state"), cookie
}

func TestOAuthFlow(t *testing.T) {
	a, _ := fakeAuth(map[string]authnv1.UserInfo{
		"sha256~sso-token": {Username: "alice"},
	})
	o := fakeOAuth(t, "sha256~sso-token", a)

	state, stateC := stateFromRedirect(t, o)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=abc&state="+url.QueryEscape(state), nil)
	req.AddCookie(stateC)
	rec := httptest.NewRecorder()
	o.Callback(rec, req)

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback should land on / (got %d → %q): %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	var session string
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && c.MaxAge >= 0 {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("no session cookie set")
	}
	tok, ok := parseCookieValue(session, secret)
	if !ok || tok != "sha256~sso-token" {
		t.Errorf("session carries %q, want the exchanged access token", tok)
	}
}

// A callback whose state doesn't match the signed cookie must never exchange the
// code — it bounces to the login screen with the generic sso_error flag.
func TestOAuthCallbackStateMismatch(t *testing.T) {
	a, _ := fakeAuth(nil)
	o := fakeOAuth(t, "whatever", a)

	_, stateC := stateFromRedirect(t, o)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=abc&state=forged", nil)
	req.AddCookie(stateC)
	rec := httptest.NewRecorder()
	o.Callback(rec, req)

	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "sso_error") {
		t.Fatalf("forged state should bounce to the login screen, got %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			t.Error("a session cookie was set despite the state mismatch")
		}
	}
}

// A surviving OAuthClient whose secret predates a reinstall passes the
// authorize probe (it never sees the secret), so ClientRegistered must prove
// the secret at the token endpoint and report the finish-SSO state.
func TestClientRegisteredStaleSecret(t *testing.T) {
	a, _ := fakeAuth(nil)
	o := fakeOAuthAt(t, a, oauthErrorStub("unauthorized_client"))
	if o.ClientRegistered(context.Background()) {
		t.Fatal("stale client secret must read as not registered")
	}
}

// invalid_grant blames only the bogus probe code: the secret matched, the
// client is registered.
func TestClientRegisteredSecretOK(t *testing.T) {
	a, _ := fakeAuth(nil)
	o := fakeOAuthAt(t, a, oauthErrorStub("invalid_grant"))
	if !o.ClientRegistered(context.Background()) {
		t.Fatal("a client the token endpoint authenticates must read as registered")
	}
}

// A real login failing the exchange with unauthorized_client is proof the
// registered secret is stale: the cached yes must drop so finish-SSO resurfaces
// without waiting out the probe TTL.
func TestOAuthCallbackStaleSecretResurfaces(t *testing.T) {
	a, _ := fakeAuth(nil)
	o := fakeOAuthAt(t, a, oauthErrorStub("unauthorized_client"))
	o.MarkRegistered()

	state, stateC := stateFromRedirect(t, o)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=abc&state="+url.QueryEscape(state), nil)
	req.AddCookie(stateC)
	rec := httptest.NewRecorder()
	o.Callback(rec, req)

	if !strings.Contains(rec.Header().Get("Location"), "sso_error") {
		t.Fatalf("failed exchange should bounce to the login screen, got %q", rec.Header().Get("Location"))
	}
	if o.ClientRegistered(context.Background()) {
		t.Error("stale-secret proof must resurface the finish-SSO state")
	}
}

// A token the cluster rejects (TokenReview says no) must not become a session,
// even though the OAuth exchange itself succeeded.
func TestOAuthCallbackRejectedToken(t *testing.T) {
	a, _ := fakeAuth(nil) // no valid tokens
	o := fakeOAuth(t, "sha256~revoked", a)

	state, stateC := stateFromRedirect(t, o)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=abc&state="+url.QueryEscape(state), nil)
	req.AddCookie(stateC)
	rec := httptest.NewRecorder()
	o.Callback(rec, req)

	if !strings.Contains(rec.Header().Get("Location"), "sso_error") {
		t.Fatalf("rejected token should bounce to the login screen, got %q", rec.Header().Get("Location"))
	}
}
