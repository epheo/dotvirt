// Delegated forge authorization: dotvirt acts in the forge with each user's own
// account, which the user connects once through the forge's OAuth2 provider.
// The forge's rules then see the person as pusher and poster, within their own
// rights there, as Kubernetes does for every other call dotvirt makes.

package auth

import (
	"context"
	"crypto/cipher"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// ForgeConfig is the OAuth2 application dotvirt is registered as at the forge.
type ForgeConfig struct {
	ClientID     string
	ClientSecret string
	// AuthorizeURL is the authorize page as browsers reach it, which may be
	// the only part of the forge they can.
	AuthorizeURL string
	// TokenURL is the token endpoint, called server to server.
	TokenURL string
	// APIURL is the forge's base URL users' tokens are sent to, from the
	// admin's spec alone: never from the forge credential Secret, which the
	// operator may write.
	APIURL      string
	RedirectURL string // {public-url}/api/forge/callback
	CAFile      string
	InsecureTLS bool
}

// forgeScopes is fixed for good: the forge refuses a grant asked with scopes
// other than the one it holds, so a change would make every user consent
// again. An API scope must always be asked: with none, the forge grants the
// token all of the user's rights.
var forgeScopes = []string{"write:repository", "read:user"}

// Forge drives the connect redirect, the code-exchange callback and the token
// renewal for delegated pull requests.
type Forge struct {
	cfg    ForgeConfig
	sealer cipher.AEAD
	client *http.Client
	store  *forgeSessions

	// The forge's reachability, for the console's automatic connect: one
	// that cannot complete would leave users on a browser error page.
	probeMu  sync.Mutex
	probedAt time.Time
	up       bool
	probing  bool      // one probe at a time; callers meanwhile read the last answer
	failedAt time.Time // the last forge call of this flow that could not reach it
}

// errForgeUnreachable marks a forge call that never got an answer (or a 5xx):
// an outage, unlike a refusal the forge answered.
var errForgeUnreachable = errors.New("the forge is unreachable")

// reachTTL is how long a reachability answer, or a failed forge call, holds.
const reachTTL = 30 * time.Second

// NewForge builds the flow. Its connections live beside a's sessions, and
// a's sign-in answers say that pull requests are delegated.
func NewForge(cfg ForgeConfig, a *Authenticator) *Forge {
	f := &Forge{cfg: cfg, sealer: a.sealer, client: tlsClient("forge", cfg.CAFile, cfg.InsecureTLS), store: newForgeSessions()}
	f.store.renew = f.renew
	a.forgeDelegated = true
	return f
}

func (f *Forge) oauth2Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     f.cfg.ClientID,
		ClientSecret: f.cfg.ClientSecret,
		RedirectURL:  f.cfg.RedirectURL,
		Scopes:       forgeScopes,
		// Never autodetected: a failed attempt is retried in the other style,
		// which would present a single-use refresh token twice.
		Endpoint: oauth2.Endpoint{AuthURL: f.cfg.AuthorizeURL, TokenURL: f.cfg.TokenURL, AuthStyle: oauth2.AuthStyleInHeader},
	}
}

// forgeStatePath scopes the connect's state cookie to the forge flow.
const forgeStatePath = "/api/forge"

// forgeStateCookie prefixes the cookie carrying a connect's state, one per
// connect, named by its state like a sign-in's.
const forgeStateCookie = "dotvirt_forge_state"

func forgeStateCookieName(state string) string { return forgeStateCookie + "_" + state }

// Connect (GET /api/forge/connect) sends a signed-in user's browser to the
// forge's authorize page with a fresh state and PKCE challenge, sealed in the
// state cookie with the session it is for and the ?return= page (SafeReturn).
// It marks the session asked, so an abandoned connect is never restarted by
// the console on its own.
func (f *Forge) Connect(w http.ResponseWriter, r *http.Request) {
	id, _ := FromContext(r.Context())
	key, ok := forgeKey(id)
	if !ok {
		// A bearer header has no browser session to connect.
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	f.store.markAsked(key, id.Issued)
	state, verifier := randomToken(), oauth2.GenerateVerifier()
	sealed := withReturn(strings.Join([]string{state, verifier, sessionTag(key)}, "."), r.URL.Query().Get("return"))
	http.SetCookie(w, &http.Cookie{
		Name:     forgeStateCookieName(state),
		Value:    seal([]byte(sealed), forgeStatePurpose, f.sealer),
		Path:     forgeStatePath,
		HttpOnly: true,
		Secure:   isTLS(r),
		// Lax: the cookie must ride the top-level redirect back from the forge.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300,
	})
	http.Redirect(w, r, f.oauth2Config().AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// sessionTag binds a connect to the console session that started it, so a
// callback cannot hand one session's forge account to another, even another
// session of the same user or of the same pasted token.
func sessionTag(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

// Callback (GET /api/forge/callback) checks the state against the session,
// exchanges the code, names the forge account the token acts as, and stores
// the connection for the session. Any failure lands on the page the connect
// started from (or the console's root), connected or not; the detail is only
// logged, never the forge's tokens or codes.
func (f *Forge) Callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	c, cerr := r.Cookie(forgeStateCookieName(state))
	ret := "/"
	fail := func(why string, err error) {
		log.Printf("forge connect: %s: %v", why, err)
		if c != nil {
			f.clearState(w, r, c.Name)
		}
		http.Redirect(w, r, ret, http.StatusFound)
	}
	id, _ := FromContext(r.Context())
	key, ok := forgeKey(id)
	if !ok {
		fail("no console session", nil)
		return
	}
	if state == "" || cerr != nil {
		fail("state cookie missing", cerr)
		return
	}
	raw, ok := open(c.Value, cookiePrefix, forgeStatePurpose, f.sealer)
	sealed, back := splitReturn(string(raw))
	parts := strings.Split(sealed, ".")
	if !ok || len(parts) != 3 || !hmac.Equal([]byte(state), []byte(parts[0])) {
		fail("state mismatch", nil)
		return
	}
	ret = back
	if !hmac.Equal([]byte(parts[2]), []byte(sessionTag(key))) {
		fail("the connect was started by another console session", nil)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		fail("not granted", errors.New(r.URL.Query().Get("error")))
		return
	}
	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, f.client)
	tok, err := f.oauth2Config().Exchange(ctx, code, oauth2.VerifierOption(parts[1]))
	if err != nil {
		f.noteFailure(err)
		fail("code exchange", scrubbed(err))
		return
	}
	t, err := forgeTokensFrom(tok)
	if err != nil {
		fail("token response", err)
		return
	}
	login, err := f.account(r.Context(), t.access)
	if err != nil {
		if errors.Is(err, errForgeUnreachable) {
			f.noteFailure(err)
		}
		fail("name the forge account", err)
		return
	}
	f.store.connect(key, id.Issued, login, t)
	log.Printf("forge connect: %s connected forge account %s", id.Username, login)
	f.clearState(w, r, c.Name)
	http.Redirect(w, r, ret, http.StatusFound)
}

func (f *Forge) clearState(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: forgeStatePath,
		HttpOnly: true, Secure: isTLS(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// account names the forge account access acts as (GET /api/v1/user).
func (f *Forge) account(ctx context.Context, access string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(f.cfg.APIURL, "/")+"/api/v1/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+access)
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errForgeUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return "", fmt.Errorf("%w: it answered %s", errForgeUnreachable, resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the forge answered %s", resp.Status)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&u); err != nil || u.Login == "" {
		return "", errors.New("the forge named no account")
	}
	return u.Login, nil
}

// renew trades refresh for the connection's next tokens. A refusal of the
// grant or of this client wraps errGrantEnded.
func (f *Forge) renew(ctx context.Context, refresh string) (forgeTokens, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, f.client)
	tok, err := f.oauth2Config().TokenSource(ctx, &oauth2.Token{RefreshToken: refresh}).Token()
	if err != nil {
		var rerr *oauth2.RetrieveError
		if errors.As(err, &rerr) && grantRefused(rerr.ErrorCode) {
			return forgeTokens{}, fmt.Errorf("%w: %s", errGrantEnded, rerr.ErrorCode)
		}
		f.noteFailure(err)
		return forgeTokens{}, scrubbed(err)
	}
	t, err := forgeTokensFrom(tok)
	if err != nil {
		return forgeTokens{}, fmt.Errorf("%w: %w", errGrantEnded, err)
	}
	return t, nil
}

// scrubbed is err as a log may carry it: a token endpoint's refusal by status
// and error code only, never its body.
func scrubbed(err error) error {
	var rerr *oauth2.RetrieveError
	if errors.As(err, &rerr) {
		status := 0
		if rerr.Response != nil {
			status = rerr.Response.StatusCode
		}
		return fmt.Errorf("the forge's token endpoint answered %d %s", status, rerr.ErrorCode)
	}
	return err
}

func forgeTokensFrom(tok *oauth2.Token) (forgeTokens, error) {
	if tok.AccessToken == "" {
		return forgeTokens{}, errors.New("no access token in the response")
	}
	expiry := tok.Expiry
	if expiry.IsZero() {
		// The forge states none: treat it as its default hour, renewed early.
		expiry = time.Now().Add(time.Hour)
	}
	return forgeTokens{access: tok.AccessToken, refresh: tok.RefreshToken, expiry: expiry}, nil
}

// noteFailure records a forge call of this flow that could not reach the
// forge (a transport error or a 5xx), which holds the automatic connect back.
// A caller hanging up is no outage, nor is a refusal the forge answered.
func (f *Forge) noteFailure(err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	var rerr *oauth2.RetrieveError
	if errors.As(err, &rerr) && rerr.Response != nil && rerr.Response.StatusCode < 500 {
		return
	}
	f.probeMu.Lock()
	f.failedAt = time.Now()
	f.probeMu.Unlock()
}

// reachable reports whether the forge answers dotvirt now: false within
// reachTTL of a failed call, else a cached probe of its API (any answer below
// 500 is reachable). The browser reaches the authorize page through another
// address, so this only approximates it; a forge whose API is down never
// gets users sent to it on their own.
func (f *Forge) reachable(ctx context.Context) bool {
	f.probeMu.Lock()
	now := time.Now()
	switch {
	case now.Sub(f.failedAt) < reachTTL:
		f.probeMu.Unlock()
		return false
	case now.Sub(f.probedAt) < reachTTL || f.probing:
		up := f.up
		f.probeMu.Unlock()
		return up
	}
	f.probing = true
	f.probeMu.Unlock()

	// Detached: the answer is cached for every session, so a caller hanging up
	// mid-probe must not record the forge as down.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	up := false
	if req, err := http.NewRequestWithContext(pctx, http.MethodGet, strings.TrimRight(f.cfg.APIURL, "/")+"/api/v1/version", nil); err == nil {
		if resp, err := f.client.Do(req); err == nil {
			resp.Body.Close()
			up = resp.StatusCode < 500
		}
	}
	f.probeMu.Lock()
	f.up, f.probedAt, f.probing = up, time.Now(), false
	f.probeMu.Unlock()
	return up
}

// ForgeStatus is a console session's forge connection, as the UI shows it.
type ForgeStatus struct {
	Connected bool   `json:"connected"`
	Login     string `json:"login,omitempty"`
	// Asked: a connect already started in this session, so the console starts
	// none on its own.
	Asked bool `json:"asked"`
	// Reachable: the forge answers, so an automatic connect can complete.
	// Probed only for a session the console would connect on its own.
	Reachable bool `json:"reachable"`
}

// Status reports id's connection. A bearer header holds none.
func (f *Forge) Status(ctx context.Context, id Identity) ForgeStatus {
	key, ok := forgeKey(id)
	if !ok {
		return ForgeStatus{}
	}
	connected, login, asked := f.store.status(key)
	st := ForgeStatus{Connected: connected, Login: login, Asked: asked}
	if !connected && !asked {
		st.Reachable = f.reachable(ctx)
	}
	return st
}

// Disconnect drops id's forge tokens. The grant stays at the forge: only the
// user, the forge's admin, or deleting the forge account revokes it.
func (f *Forge) Disconnect(id Identity) {
	if key, ok := forgeKey(id); ok {
		f.store.disconnect(key)
	}
}

// Held is how many console sessions hold a forge entry, connected or asked.
func (f *Forge) Held() int {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	return len(f.store.m)
}

// Forget drops the connection of id's console session (sign-out).
func (f *Forge) Forget(id Identity) {
	if key, ok := forgeKey(id); ok {
		f.store.forget(key)
	}
}

// ForgeCredential is what a delegated forge action runs with: the forge
// account and its current access token.
type ForgeCredential struct {
	Login string
	Token string
}

// Credential returns id's forge credential, renewed when due, or
// ErrForgeNotConnected.
func (f *Forge) Credential(ctx context.Context, id Identity) (ForgeCredential, error) {
	key, ok := forgeKey(id)
	if !ok {
		return ForgeCredential{}, ErrForgeNotConnected
	}
	login, token, err := f.store.token(ctx, key)
	if err != nil {
		return ForgeCredential{}, err
	}
	return ForgeCredential{Login: login, Token: token}, nil
}
