package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
)

// fakeOAuth wires the flow over a stub token endpoint and a pre-seeded
// discovery document (the fake clientset cannot serve the well-known path).
// The stub returns accessToken for any code, mirroring osin's happy path.
func fakeOAuth(t *testing.T, accessToken string, a *Authenticator) *OAuth {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": accessToken, "token_type": "Bearer",
		})
	}))
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
