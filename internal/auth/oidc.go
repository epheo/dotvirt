// OpenID Connect sign-in, against any issuer the apiserver trusts. Only token
// acquisition differs from a pasted token: the issuer's ID token is the user's
// bearer for the apiserver, whose AuthenticationConfiguration checks its
// audience is this client, so it rides the same TokenReview and pass-through
// path. An issuer's tokens may live minutes, not OpenShift's day, so each
// session's tokens stay server-side and are renewed there (oidcSessions).

package auth

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// OIDCConfig is the client dotvirt is registered as at the issuer.
type OIDCConfig struct {
	// Issuer is the issuer URL exactly as its tokens and discovery name it.
	Issuer   string
	ClientID string
	// ClientSecret is empty for a public client, which PKCE alone protects.
	ClientSecret string
	RedirectURL  string   // {public-url}/api/auth/callback
	Scopes       []string // asked beside openid
	// Name is what the sign-in button names the issuer.
	Name        string
	CAFile      string
	InsecureTLS bool
}

// OIDC drives the sign-in redirect, the code-exchange callback, token renewal
// and revocation. Endpoints come from the issuer's discovery document, fetched
// lazily, so an unreachable issuer never blocks startup.
type OIDC struct {
	cfg    OIDCConfig
	auth   *Authenticator
	client *http.Client
	disc   discovery[oidcMeta]
}

type oidcMeta struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

// NewOIDC builds the flow and hands a's sessions its renewal.
func NewOIDC(cfg OIDCConfig, a *Authenticator) *OIDC {
	o := &OIDC{cfg: cfg, auth: a, client: tlsClient("oidc", cfg.CAFile, cfg.InsecureTLS)}
	a.oidc.renew = o.renew
	return o
}

// Name is what the sign-in button names the issuer.
func (o *OIDC) Name() string {
	return o.cfg.Name
}

func (o *OIDC) discover(ctx context.Context) (*oidcMeta, error) {
	return o.disc.get(ctx, true, o.fetchMeta)
}

func (o *OIDC) fetchMeta(ctx context.Context) (*oidcMeta, error) {
	u := strings.TrimRight(o.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc discovery: %s answered %s", u, resp.Status)
	}
	var m oidcMeta
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&m); err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	// The apiserver matches the tokens' iss against its own configured issuer,
	// exactly: a mismatch here would fail every sign-in after the redirect.
	if m.Issuer != o.cfg.Issuer {
		return nil, fmt.Errorf("oidc discovery: the issuer names itself %q, configured %q", m.Issuer, o.cfg.Issuer)
	}
	if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
		return nil, errors.New("oidc discovery: no endpoints in the discovery document")
	}
	return &m, nil
}

func (o *OIDC) oauth2Config(m *oidcMeta) *oauth2.Config {
	scopes := []string{"openid"}
	for _, s := range o.cfg.Scopes {
		if s != "openid" {
			scopes = append(scopes, s)
		}
	}
	// Never autodetected: a failed attempt is retried in the other style, which
	// would present a single-use refresh token twice.
	style := oauth2.AuthStyleInHeader
	if o.cfg.ClientSecret == "" {
		style = oauth2.AuthStyleInParams
	}
	return &oauth2.Config{
		ClientID:     o.cfg.ClientID,
		ClientSecret: o.cfg.ClientSecret,
		RedirectURL:  o.cfg.RedirectURL,
		Scopes:       scopes,
		Endpoint:     oauth2.Endpoint{AuthURL: m.AuthorizationEndpoint, TokenURL: m.TokenEndpoint, AuthStyle: style},
	}
}

// LoginRedirect (GET /api/auth/oidc) sends the browser to the issuer with a
// fresh state, PKCE challenge and nonce, their secrets sealed in the state
// cookie.
func (o *OIDC) LoginRedirect(w http.ResponseWriter, r *http.Request) {
	m, err := o.discover(r.Context())
	if err != nil {
		log.Printf("oidc: %v", err)
		http.Error(w, "single sign-on is unavailable, try again shortly", http.StatusServiceUnavailable)
		return
	}
	state, nonce, verifier := randomToken(), randomToken(), oauth2.GenerateVerifier()
	setStateCookie(w, r, strings.Join([]string{state, verifier, nonce}, "."), o.auth.sealer)
	http.Redirect(w, r, o.oauth2Config(m).AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", nonce)), http.StatusFound)
}

// Callback (GET /api/auth/callback) checks the state, exchanges the code for
// the user's tokens, validates the ID token exactly like a pasted token, and
// starts a session holding them. Failures land back on the login screen with
// the generic sso_error flag; the detail is only logged.
func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) {
	failLogin := func(why string, err error) {
		log.Printf("oidc callback: %s: %v", why, err)
		clearStateCookie(w, r)
		http.Redirect(w, r, "/?sso_error=1", http.StatusFound)
	}
	c, err := r.Cookie(stateCookie)
	if err != nil {
		failLogin("state cookie missing", err)
		return
	}
	sealed, ok := parseStateValue(c.Value, o.auth.sealer)
	parts := strings.Split(sealed, ".")
	state := r.URL.Query().Get("state")
	if !ok || len(parts) != 3 || state == "" || !hmac.Equal([]byte(state), []byte(parts[0])) {
		failLogin("state mismatch", nil)
		return
	}
	verifier, nonce := parts[1], parts[2]
	code := r.URL.Query().Get("code")
	if code == "" {
		failLogin("no code (user denied or issuer error)", errors.New(r.URL.Query().Get("error")))
		return
	}
	m, err := o.discover(r.Context())
	if err != nil {
		failLogin("discovery", err)
		return
	}
	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, o.client)
	tok, err := o.oauth2Config(m).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		failLogin("code exchange", err)
		return
	}
	t, err := tokensFrom(tok)
	if err != nil {
		failLogin("token response", err)
		return
	}
	if claims, _ := readClaims(t.id); !hmac.Equal([]byte(claims.Nonce), []byte(nonce)) {
		failLogin("nonce mismatch", nil)
		return
	}
	id, err := o.auth.Validate(r.Context(), t.id)
	if err != nil {
		failLogin("token review", err)
		return
	}
	if t.refresh == "" {
		log.Printf("oidc: the issuer gave %s no refresh token; the session ends with its ID token", id.Username)
	}
	log.Printf("oidc: %s signed in", id.Username)
	now := time.Now()
	handle := o.auth.oidc.start(t, now)
	clearStateCookie(w, r)
	setCookie(w, r, session{token: handle, issued: now, sso: true, oidc: true}, o.auth.sealer)
	http.Redirect(w, r, "/", http.StatusFound)
}

// renew trades refresh for the session's next tokens. A refusal of the grant
// or of this client wraps errGrantEnded; anything else may pass.
func (o *OIDC) renew(ctx context.Context, refresh string) (oidcTokens, error) {
	m, err := o.discover(ctx)
	if err != nil {
		return oidcTokens{}, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, o.client)
	tok, err := o.oauth2Config(m).TokenSource(ctx, &oauth2.Token{RefreshToken: refresh}).Token()
	if err != nil {
		var rerr *oauth2.RetrieveError
		if errors.As(err, &rerr) && grantRefused(rerr.ErrorCode) {
			return oidcTokens{}, fmt.Errorf("%w: %s", errGrantEnded, rerr.ErrorCode)
		}
		return oidcTokens{}, err
	}
	t, err := tokensFrom(tok)
	if err != nil {
		return oidcTokens{}, fmt.Errorf("%w: %w", errGrantEnded, err)
	}
	return t, nil
}

// grantRefused: the RFC 6749 codes that no retry turns into tokens.
func grantRefused(code string) bool {
	return code == "invalid_grant" || code == "invalid_client" || code == "unauthorized_client"
}

// Revoke asks the issuer to revoke the refresh token of the session id that
// Logout ended (RFC 7009), when the issuer offers revocation.
func (o *OIDC) Revoke(ctx context.Context, id Identity) error {
	if id.refresh == "" {
		return nil
	}
	m, err := o.discover(ctx)
	if err != nil || m.RevocationEndpoint == "" {
		return err
	}
	form := url.Values{"token": {id.refresh}, "token_type_hint": {"refresh_token"}}
	if o.cfg.ClientSecret == "" {
		form.Set("client_id", o.cfg.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if o.cfg.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(o.cfg.ClientID), url.QueryEscape(o.cfg.ClientSecret))
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("revocation answered %s", resp.Status)
	}
	return nil
}

// tokensFrom reads a session's tokens out of a token response.
func tokensFrom(tok *oauth2.Token) (oidcTokens, error) {
	id, _ := tok.Extra("id_token").(string)
	if id == "" {
		return oidcTokens{}, errors.New("no id_token in the token response")
	}
	claims, err := readClaims(id)
	if err != nil {
		return oidcTokens{}, err
	}
	return oidcTokens{id: id, refresh: tok.RefreshToken, expiry: time.Unix(claims.Exp, 0)}, nil
}

// idClaims are the ID token claims dotvirt reads. Unverified: the apiserver
// checks the signature, issuer and audience on every call. dotvirt reads only
// when to renew, and the nonce the token endpoint, reached over TLS, echoes.
type idClaims struct {
	Exp   int64  `json:"exp"`
	Nonce string `json:"nonce"`
}

func readClaims(token string) (idClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return idClaims{}, errors.New("the id_token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return idClaims{}, fmt.Errorf("the id_token's claims: %w", err)
	}
	var c idClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return idClaims{}, fmt.Errorf("the id_token's claims: %w", err)
	}
	if c.Exp == 0 {
		return idClaims{}, errors.New("the id_token has no exp")
	}
	return c, nil
}
