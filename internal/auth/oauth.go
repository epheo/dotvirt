// OpenShift SSO: the authorization-code flow against the cluster's OAuth server.
// Only token ACQUISITION changes — the access token OpenShift returns is a normal
// bearer token for the API server, so it lands in the exact same TokenReview +
// signed-cookie + per-request pass-through path as a pasted token, and cluster
// RBAC stays the sole authority. Token paste remains for vanilla Kubernetes and
// ServiceAccounts.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"k8s.io/client-go/kubernetes"
)

// OAuthConfig wires the OpenShift OAuthClient dotvirt was registered as.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string // {public-url}/api/auth/callback — must match the OAuthClient's redirectURIs
	// CAFile is a PEM bundle to trust for the token endpoint (the oauth Route is
	// usually signed by the ingress CA, not in the pod's system pool). Empty =
	// system pool; InsecureTLS covers dev.
	CAFile      string
	InsecureTLS bool
}

// OAuth drives the login redirect and the code-exchange callback. Endpoints come
// from the API server's /.well-known/oauth-authorization-server document —
// discovered lazily and cached, so a slow OAuth stack never blocks startup and a
// vanilla-Kubernetes cluster simply 503s the (unreachable) SSO route.
type OAuth struct {
	cfg    OAuthConfig
	saKube kubernetes.Interface // discovery rides dotvirt's SA (an unauthenticated cluster metadata read)
	auth   *Authenticator
	client *http.Client // token exchange; carries the CA the oauth Route is signed with

	mu   sync.Mutex
	meta *oauthMeta
}

type oauthMeta struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// NewOAuth builds the flow. It never probes the cluster here — see discover.
func NewOAuth(cfg OAuthConfig, saKube kubernetes.Interface, auth *Authenticator) (*OAuth, error) {
	transport := http.DefaultTransport
	if cfg.CAFile != "" || cfg.InsecureTLS {
		tlsCfg := &tls.Config{InsecureSkipVerify: cfg.InsecureTLS} //nolint:gosec // explicit dev opt-in
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("oauth ca: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("oauth ca: no certificates in %s", cfg.CAFile)
			}
			tlsCfg.RootCAs = pool
		}
		transport = &http.Transport{TLSClientConfig: tlsCfg}
	}
	return &OAuth{
		cfg:    cfg,
		saKube: saKube,
		auth:   auth,
		client: &http.Client{Transport: transport, Timeout: 15 * time.Second},
	}, nil
}

// discover fetches (once, then cached) the OAuth endpoints from the API server's
// well-known document. Reached through the SA client so TLS + auth to the API
// server need no extra wiring.
func (o *OAuth) discover(ctx context.Context) (*oauthMeta, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.meta != nil {
		return o.meta, nil
	}
	raw, err := o.saKube.Discovery().RESTClient().Get().
		AbsPath("/.well-known/oauth-authorization-server").DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("oauth discovery: %w", err)
	}
	var m oauthMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("oauth discovery: %w", err)
	}
	if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
		return nil, errors.New("oauth discovery: no endpoints in well-known document")
	}
	o.meta = &m
	return o.meta, nil
}

func (o *OAuth) oauth2Config(m *oauthMeta) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     o.cfg.ClientID,
		ClientSecret: o.cfg.ClientSecret,
		RedirectURL:  o.cfg.RedirectURL,
		// user:full: the token acts as the user, which is dotvirt's entire model.
		Scopes:   []string{"user:full"},
		Endpoint: oauth2.Endpoint{AuthURL: m.AuthorizationEndpoint, TokenURL: m.TokenEndpoint},
	}
}

// stateCookie carries the CSRF state across the round-trip to the OAuth server,
// HMAC-signed with the session secret like the session cookie itself.
const stateCookie = "dotvirt_oauth_state"

// LoginRedirect (GET /api/auth/openshift) sends the browser to the cluster's
// authorize endpoint with a fresh signed state.
func (o *OAuth) LoginRedirect(w http.ResponseWriter, r *http.Request) {
	m, err := o.discover(r.Context())
	if err != nil {
		http.Error(w, "OpenShift SSO unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    cookieValue(state, o.auth.secret),
		Path:     "/api/auth",
		HttpOnly: true,
		Secure:   isTLS(r),
		// Lax: the cookie must ride the top-level redirect back from the OAuth server.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300,
	})
	http.Redirect(w, r, o.oauth2Config(m).AuthCodeURL(state), http.StatusFound)
}

// Callback (GET /api/auth/callback) verifies the state, exchanges the code for
// the user's access token, validates it exactly like a pasted token, and sets the
// same session cookie. Failures land back on the login screen with a generic
// sso_error flag — the detail is logged, not shown (it can carry endpoint URLs).
func (o *OAuth) Callback(w http.ResponseWriter, r *http.Request) {
	failLogin := func(why string, err error) {
		log.Printf("oauth callback: %s: %v", why, err)
		clearStateCookie(w, r)
		http.Redirect(w, r, "/?sso_error=1", http.StatusFound)
	}
	c, err := r.Cookie(stateCookie)
	if err != nil {
		failLogin("state cookie missing", err)
		return
	}
	want, ok := parseCookieValue(c.Value, o.auth.secret)
	state := r.URL.Query().Get("state")
	if !ok || state == "" || !hmac.Equal([]byte(state), []byte(want)) {
		failLogin("state mismatch", nil)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		failLogin("no code (user denied or oauth error)", errors.New(r.URL.Query().Get("error")))
		return
	}
	m, err := o.discover(r.Context())
	if err != nil {
		failLogin("discovery", err)
		return
	}
	// The exchange must use the CA-aware client — the token endpoint is the
	// external oauth Route, not the API server.
	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, o.client)
	tok, err := o.oauth2Config(m).Exchange(ctx, code)
	if err != nil {
		failLogin("code exchange", err)
		return
	}
	id, err := o.auth.Validate(r.Context(), tok.AccessToken)
	if err != nil {
		failLogin("token review", err)
		return
	}
	log.Printf("oauth: %s signed in via OpenShift SSO", id.Username)
	clearStateCookie(w, r)
	setCookie(w, r, tok.AccessToken, o.auth.secret)
	http.Redirect(w, r, "/", http.StatusFound)
}

func clearStateCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: "", Path: "/api/auth",
		HttpOnly: true, Secure: isTLS(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}
