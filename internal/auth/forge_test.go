package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
	authnv1 "k8s.io/api/authentication/v1"
)

// fakeForge is a forge's OAuth2 provider: codes bound to their PKCE challenge,
// single-use rotating refresh tokens, and /api/v1/user for the access tokens.
type fakeForge struct {
	srv *httptest.Server

	mu         sync.Mutex
	challenges map[string]string // code -> S256 challenge
	access     map[string]bool
	refresh    map[string]bool // live refresh tokens
	spent      map[string]bool
	refreshes  int
	refuse     bool          // answer invalid_grant to refreshes
	expiresIn  time.Duration // access token lifetime
	n          int
}

func newFakeForge(t *testing.T) *fakeForge {
	f := &fakeForge{challenges: map[string]string{}, access: map[string]bool{}, refresh: map[string]bool{}, spent: map[string]bool{}, expiresIn: time.Hour}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", f.token)
	mux.HandleFunc("GET /api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := f.access[strings.TrimPrefix(r.Header.Get("Authorization"), "token ")]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "alice-forge"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// authorize stands in for the user consenting on the forge's page: it
// records the challenge the connect sent and returns the code.
func (f *fakeForge) authorize(t *testing.T, location string) (code, state string) {
	t.Helper()
	u := must(url.Parse(location))
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("connect sent no S256 PKCE challenge: %s", location)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	code = fmt.Sprintf("code-%d", f.n)
	f.challenges[code] = q.Get("code_challenge")
	return code, q.Get("state")
}

func (f *fakeForge) mint() (string, string) {
	f.n++
	a, r := fmt.Sprintf("access-%d", f.n), fmt.Sprintf("refresh-%d", f.n)
	f.access[a], f.refresh[r] = true, true
	return a, r
}

func (f *fakeForge) token(w http.ResponseWriter, r *http.Request) {
	if id, secret, ok := r.BasicAuth(); !ok || id != "dotvirt" || secret != "s3cret" {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		challenge, ok := f.challenges[r.Form.Get("code")]
		delete(f.challenges, r.Form.Get("code"))
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			tokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
	case "refresh_token":
		f.refreshes++
		rt := r.Form.Get("refresh_token")
		if f.refuse || !f.refresh[rt] || f.spent[rt] {
			tokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		f.spent[rt] = true
	default:
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	a, rt := f.mint()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": a, "refresh_token": rt, "token_type": "bearer", "expires_in": int(f.expiresIn.Seconds()),
	})
}

type forgeRig struct {
	forge *fakeForge
	a     *Authenticator
	f     *Forge
	h     http.Handler
	seen  Identity
}

func newForgeRig(t *testing.T) *forgeRig {
	a, _ := fakeAuth(map[string]authnv1.UserInfo{
		"tok-alice":  {Username: "alice"},
		"tok-alice2": {Username: "alice"},
		"tok-bob":    {Username: "bob"},
	})
	ff := newFakeForge(t)
	rig := &forgeRig{forge: ff, a: a}
	rig.f = NewForge(ForgeConfig{
		ClientID: "dotvirt", ClientSecret: "s3cret",
		AuthorizeURL: "https://forge.example/login/oauth/authorize",
		TokenURL:     ff.srv.URL + "/login/oauth/access_token",
		APIURL:       ff.srv.URL,
		RedirectURL:  "https://console.example/api/forge/callback",
	}, a)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/forge/connect", rig.f.Connect)
	mux.HandleFunc("GET /api/forge/callback", rig.f.Callback)
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		rig.seen, _ = FromContext(r.Context())
	})
	rig.h = a.Middleware(mux)
	return rig
}

// signIn returns a console session cookie for token.
func (rig *forgeRig) signIn(t *testing.T, token string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	rig.a.Login(rec, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"token":"`+token+`"}`)))
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	t.Fatalf("sign-in set no session cookie: %d", rec.Code)
	return nil
}

func (rig *forgeRig) get(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, req)
	return rec
}

// identity is the Identity the middleware gives session's requests.
func (rig *forgeRig) identity(t *testing.T, session *http.Cookie) Identity {
	t.Helper()
	rig.get("/api/me", session)
	return rig.seen
}

// connect runs a connect from page and returns the state cookie and the
// forge's code and state, as the browser would carry them back.
func (rig *forgeRig) connect(t *testing.T, session *http.Cookie, page string) (*http.Cookie, string, string) {
	t.Helper()
	rec := rig.get("/api/forge/connect?return="+url.QueryEscape(page), session)
	if rec.Code != http.StatusFound {
		t.Fatalf("connect: status %d", rec.Code)
	}
	var state *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if strings.HasPrefix(c.Name, forgeStateCookie+"_") {
			state = c
		}
	}
	if state == nil || state.Path != forgeStatePath || !state.HttpOnly {
		t.Fatalf("connect set no scoped, httpOnly state cookie: %+v", state)
	}
	code, st := rig.forge.authorize(t, rec.Header().Get("Location"))
	return state, code, st
}

// keyOf is the store key of id's console session.
func keyOf(t *testing.T, id Identity) string {
	t.Helper()
	k, ok := forgeKey(id)
	if !ok {
		t.Fatal("no console session")
	}
	return k
}

func (rig *forgeRig) callback(t *testing.T, session, state *http.Cookie, query string) *httptest.ResponseRecorder {
	t.Helper()
	return rig.get("/api/forge/callback?"+query, session, state)
}

func TestForgeConnectRoundTrip(t *testing.T) {
	rig := newForgeRig(t)
	session := rig.signIn(t, "tok-alice")
	state, code, st := rig.connect(t, session, "/compute/vms?x=1")
	rec := rig.callback(t, session, state, "code="+code+"&state="+st)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/compute/vms?x=1" {
		t.Fatalf("callback: %d to %q, want the page the connect started from", rec.Code, rec.Header().Get("Location"))
	}
	id := rig.identity(t, session)
	if s := rig.f.Status(context.Background(), id); !s.Connected || s.Login != "alice-forge" || !s.Asked {
		t.Fatalf("status after connect = %+v", s)
	}
	cred, err := rig.f.Credential(context.Background(), id)
	if err != nil || cred.Login != "alice-forge" || !strings.HasPrefix(cred.Token, "access-") {
		t.Fatalf("credential = %+v, %v", cred, err)
	}
}

func TestForgeConnectAsksFixedScopesAtTheAuthorizeURL(t *testing.T) {
	rig := newForgeRig(t)
	rec := rig.get("/api/forge/connect", rig.signIn(t, "tok-alice"))
	u := must(url.Parse(rec.Header().Get("Location")))
	q := u.Query()
	if u.Scheme+"://"+u.Host+u.Path != "https://forge.example/login/oauth/authorize" {
		t.Errorf("connect went to %s, want the configured authorize URL", u)
	}
	if q.Get("scope") != "write:repository read:user" || q.Get("client_id") != "dotvirt" ||
		q.Get("redirect_uri") != "https://console.example/api/forge/callback" || q.Get("response_type") != "code" {
		t.Errorf("authorize query = %v", q)
	}
}

func TestForgeCallbackChecksTheState(t *testing.T) {
	rig := newForgeRig(t)
	session := rig.signIn(t, "tok-alice")
	state, code, st := rig.connect(t, session, "/storage")
	for name, query := range map[string]string{
		"another state": "code=" + code + "&state=not-" + st,
		"no state":      "code=" + code,
	} {
		rec := rig.callback(t, session, state, query)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
			t.Errorf("%s: %d to %q, want the root", name, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := rig.get("/api/forge/callback?code="+code+"&state="+st, session); rec.Header().Get("Location") != "/" {
		t.Errorf("no state cookie: landed on %q", rec.Header().Get("Location"))
	}
	if rig.f.Status(context.Background(), rig.identity(t, session)).Connected {
		t.Fatal("a callback that failed its state check connected the session")
	}
}

func TestForgeCallbackBindsTheConnectToItsSession(t *testing.T) {
	rig := newForgeRig(t)
	alice, bob := rig.signIn(t, "tok-alice"), rig.signIn(t, "tok-bob")
	state, code, st := rig.connect(t, alice, "/")
	rig.callback(t, bob, state, "code="+code+"&state="+st)
	if rig.f.Status(context.Background(), rig.identity(t, bob)).Connected || rig.f.Status(context.Background(), rig.identity(t, alice)).Connected {
		t.Fatal("a connect started by one session connected another")
	}
}

func TestForgeCallbackNeedsTheVerifier(t *testing.T) {
	rig := newForgeRig(t)
	session := rig.signIn(t, "tok-alice")
	_, code, _ := rig.connect(t, session, "/")
	// The forge bound the code to its connect's challenge: another connect's
	// sealed verifier cannot redeem it.
	other, _, otherState := rig.connect(t, session, "/")
	rig.callback(t, session, other, "code="+code+"&state="+otherState)
	if rig.f.Status(context.Background(), rig.identity(t, session)).Connected {
		t.Fatal("a code redeemed with another connect's verifier")
	}
}

func TestForgeNotGrantedLandsWithoutAConnection(t *testing.T) {
	rig := newForgeRig(t)
	session := rig.signIn(t, "tok-alice")
	state, _, st := rig.connect(t, session, "/storage")
	rec := rig.callback(t, session, state, "error=access_denied&state="+st)
	if rec.Header().Get("Location") != "/storage" {
		t.Errorf("a declined connect landed on %q, want its page", rec.Header().Get("Location"))
	}
	if s := rig.f.Status(context.Background(), rig.identity(t, session)); s.Connected || !s.Asked {
		t.Fatalf("status after a declined connect = %+v, want asked and not connected", s)
	}
}

func TestForgeReturnPathIsTheConsoleOwn(t *testing.T) {
	rig := newForgeRig(t)
	session := rig.signIn(t, "tok-alice")
	state, code, st := rig.connect(t, session, "//evil.example/x")
	if loc := rig.callback(t, session, state, "code="+code+"&state="+st).Header().Get("Location"); loc != "/" {
		t.Fatalf("connect returned to %q", loc)
	}
}

func TestForgePageLoadsWithoutASessionLandOnTheRoot(t *testing.T) {
	rig := newForgeRig(t)
	for _, p := range []string{"/api/forge/connect", "/api/forge/callback?code=x&state=y"} {
		rec := rig.get(p)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
			t.Errorf("%s without a session: %d to %q, want the root", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	// Any other API path still answers a status.
	if rec := rig.get("/api/me"); rec.Code != http.StatusUnauthorized {
		t.Errorf("/api/me without a session: %d, want 401", rec.Code)
	}
}

func TestForgeRenewsOncePerConnection(t *testing.T) {
	rig := newForgeRig(t)
	session := rig.signIn(t, "tok-alice")
	state, code, st := rig.connect(t, session, "/")
	rig.callback(t, session, state, "code="+code+"&state="+st)
	id := rig.identity(t, session)
	e := rig.f.store.get(keyOf(t, id))
	e.mu.Lock()
	e.renewAt = time.Now().Add(-time.Second)
	e.mu.Unlock()

	var wg sync.WaitGroup
	tokens := make([]string, 8)
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := rig.f.Credential(context.Background(), id)
			if err != nil {
				t.Errorf("credential: %v", err)
			}
			tokens[i] = c.Token
		}()
	}
	wg.Wait()
	if rig.forge.refreshes != 1 {
		t.Fatalf("%d refreshes for one due connection, want 1 (the forge spends a refresh token once)", rig.forge.refreshes)
	}
	for _, tok := range tokens[1:] {
		if tok != tokens[0] {
			t.Fatalf("callers got different tokens after one renewal: %v", tokens)
		}
	}
}

func TestForgeRefusedRenewalDisconnects(t *testing.T) {
	rig := newForgeRig(t)
	session := rig.signIn(t, "tok-alice")
	state, code, st := rig.connect(t, session, "/")
	rig.callback(t, session, state, "code="+code+"&state="+st)
	id := rig.identity(t, session)
	e := rig.f.store.get(keyOf(t, id))
	e.mu.Lock()
	e.renewAt = time.Now().Add(-time.Second)
	e.mu.Unlock()
	rig.forge.refuse = true
	if _, err := rig.f.Credential(context.Background(), id); !errors.Is(err, ErrForgeNotConnected) {
		t.Fatalf("credential after a refused renewal: %v, want ErrForgeNotConnected", err)
	}
	if rig.f.Status(context.Background(), id).Connected {
		t.Fatal("a refused grant left the session connected")
	}
}

func TestForgeDisconnectAndSignOut(t *testing.T) {
	rig := newForgeRig(t)
	session := rig.signIn(t, "tok-alice")
	state, code, st := rig.connect(t, session, "/")
	rig.callback(t, session, state, "code="+code+"&state="+st)
	id := rig.identity(t, session)

	rig.f.Disconnect(id)
	if s := rig.f.Status(context.Background(), id); s.Connected || !s.Asked {
		t.Fatalf("after disconnect: %+v, want asked and not connected (no automatic reconnect)", s)
	}
	if _, err := rig.f.Credential(context.Background(), id); !errors.Is(err, ErrForgeNotConnected) {
		t.Fatalf("credential after disconnect: %v", err)
	}
	rig.f.Forget(id)
	if s := rig.f.Status(context.Background(), id); s.Asked {
		t.Fatal("sign-out kept the session's forge entry")
	}
}

func TestForgeConnectionEndsWithItsConsoleSession(t *testing.T) {
	f := newForgeSessions()
	issued := time.Now().Add(-sessionMaxAge - time.Minute)
	f.connect("k", issued, "alice-forge", forgeTokens{access: "a", refresh: "r", expiry: time.Now().Add(time.Hour)})
	if f.get("k") != nil {
		t.Fatal("a connection outlived its console session")
	}
	if _, _, err := f.token(context.Background(), "k"); !errors.Is(err, ErrForgeNotConnected) {
		t.Fatalf("token of an expired session: %v", err)
	}
}

func TestForgeConnectionsAreLostOnRestart(t *testing.T) {
	rig := newForgeRig(t)
	session := rig.signIn(t, "tok-alice")
	state, code, st := rig.connect(t, session, "/")
	rig.callback(t, session, state, "code="+code+"&state="+st)
	restarted := NewForge(rig.f.cfg, rig.a)
	if restarted.Status(context.Background(), rig.identity(t, session)).Connected {
		t.Fatal("a connection survived a restart")
	}
}

func TestForgeLogsNoTokens(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	err := scrubbed(&oauth2.RetrieveError{
		Response:  &http.Response{StatusCode: 400},
		Body:      []byte(`{"error":"invalid_grant","refresh_token":"secret-refresh","access_token":"secret-access"}`),
		ErrorCode: "invalid_grant",
	})
	log.Print(err)
	if out := buf.String(); strings.Contains(out, "secret-") || !strings.Contains(out, "400 invalid_grant") {
		t.Fatalf("logged %q", out)
	}
}

// Two browsers signed in with one pasted token are two console sessions: a
// connect started in one never completes in the other, and each one's
// sign-out leaves the other's connection.
func TestForgeSameTokenTwoBrowsersStayApart(t *testing.T) {
	rig := newForgeRig(t)
	a := rig.signIn(t, "tok-alice")
	time.Sleep(2 * time.Millisecond)
	b := rig.signIn(t, "tok-alice")
	ida, idb := rig.identity(t, a), rig.identity(t, b)
	if ida.Key() != idb.Key() {
		t.Fatal("the rig no longer shares the token key; this test needs it")
	}
	state, code, st := rig.connect(t, a, "/")
	rig.callback(t, b, state, "code="+code+"&state="+st)
	if rig.f.Status(context.Background(), idb).Connected || rig.f.Status(context.Background(), ida).Connected {
		t.Fatal("a connect started in one browser completed in the other")
	}
	if _, err := rig.f.Credential(context.Background(), idb); !errors.Is(err, ErrForgeNotConnected) {
		t.Fatalf("the other browser got a credential: %v", err)
	}
	stateB, codeB, stB := rig.connect(t, b, "/")
	rig.callback(t, b, stateB, "code="+codeB+"&state="+stB)
	rig.f.Forget(ida)
	if !rig.f.Status(context.Background(), idb).Connected {
		t.Fatal("one browser's sign-out dropped the other's connection")
	}
}

// Another console session of the same user is still another session.
func TestForgeSameUserOtherSessionStaysApart(t *testing.T) {
	rig := newForgeRig(t)
	a, b := rig.signIn(t, "tok-alice"), rig.signIn(t, "tok-alice2")
	state, code, st := rig.connect(t, a, "/")
	rig.callback(t, b, state, "code="+code+"&state="+st)
	if rig.f.Status(context.Background(), rig.identity(t, b)).Connected {
		t.Fatal("a connect completed in another session of the same user")
	}
}

// A bearer header is no console session: it never reads a connection.
func TestForgeBearerHeaderHoldsNoConnection(t *testing.T) {
	rig := newForgeRig(t)
	session := rig.signIn(t, "tok-alice")
	state, code, st := rig.connect(t, session, "/")
	rig.callback(t, session, state, "code="+code+"&state="+st)
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer tok-alice")
	rig.h.ServeHTTP(httptest.NewRecorder(), req)
	bearer := rig.seen
	if !bearer.Issued.IsZero() || bearer.Username != "alice" {
		t.Fatalf("bearer identity = %+v", bearer)
	}
	if rig.f.Status(context.Background(), bearer).Connected {
		t.Fatal("a bearer header read the session's connection")
	}
	if _, err := rig.f.Credential(context.Background(), bearer); !errors.Is(err, ErrForgeNotConnected) {
		t.Fatalf("a bearer header got a credential: %v", err)
	}
	if rec := rig.get("/api/forge/connect"); rec.Header().Get("Location") != "/" {
		t.Errorf("a connect without a console session went to %q", rec.Header().Get("Location"))
	}
	held := rig.f.Held()
	connect := httptest.NewRequest("GET", "/api/forge/connect", nil)
	connect.Header.Set("Authorization", "Bearer tok-alice")
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, connect)
	if rec.Header().Get("Location") != "/" || rig.f.Held() != held {
		t.Errorf("a bearer header started a connect: to %q, %d entries", rec.Header().Get("Location"), rig.f.Held())
	}
}

// An expired access token without a refresh token is never served.
func TestForgeExpiredTokenWithoutRefreshIsNotServed(t *testing.T) {
	f := newForgeSessions()
	f.connect("k", time.Now(), "alice-forge", forgeTokens{access: "a", expiry: time.Now().Add(-time.Minute)})
	if _, _, err := f.token(context.Background(), "k"); !errors.Is(err, ErrForgeNotConnected) {
		t.Fatalf("an expired token with no refresh: %v, want ErrForgeNotConnected", err)
	}
	if connected, _, _ := f.status("k"); connected {
		t.Fatal("an expired token with no refresh reads connected")
	}
}

// dueSessions is a store with one connection under "k" whose renewal is due
// and blocks until release closes, timed out after a second so a regression
// fails rather than hangs.
func dueSessions(t *testing.T) (*forgeSessions, chan struct{}) {
	t.Helper()
	f := newForgeSessions()
	release := make(chan struct{})
	f.renew = func(context.Context, string) (forgeTokens, error) {
		select {
		case <-release:
		case <-time.After(time.Second):
		}
		return forgeTokens{access: "next", refresh: "r2", expiry: time.Now().Add(time.Hour)}, nil
	}
	f.connect("k", time.Now(), "alice-forge", forgeTokens{access: "a", refresh: "r", expiry: time.Now().Add(time.Hour)})
	e := f.get("k")
	e.mu.Lock()
	e.renewAt = time.Now().Add(-time.Second)
	e.mu.Unlock()
	return f, release
}

// A status read never waits on a renewal in flight.
func TestForgeStatusDoesNotWaitOnARenewal(t *testing.T) {
	f, release := dueSessions(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = f.token(context.Background(), "k")
	}()
	time.Sleep(20 * time.Millisecond) // the renewal is now in flight
	start := time.Now()
	if connected, _, _ := f.status("k"); !connected {
		t.Error("status during a renewal: not connected")
	}
	if waited := time.Since(start); waited > 100*time.Millisecond {
		t.Errorf("status waited %v on the renewal", waited)
	}
	close(release)
	<-done
}

// A disconnect landing during a renewal wins: the renewal's tokens never
// reconnect the session.
func TestForgeDisconnectDuringARenewalWins(t *testing.T) {
	f, release := dueSessions(t)
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, err = f.token(context.Background(), "k")
	}()
	time.Sleep(20 * time.Millisecond)
	f.disconnect("k")
	close(release)
	<-done
	if !errors.Is(err, ErrForgeNotConnected) {
		t.Errorf("the renewal's caller got %v, want ErrForgeNotConnected", err)
	}
	if connected, _, _ := f.status("k"); connected {
		t.Fatal("a renewal reconnected a session disconnected meanwhile")
	}
}

// The automatic connect needs a forge that answers: a probe of its API, and
// any recent failed call of the flow, hold it back.
func TestForgeReachability(t *testing.T) {
	rig := newForgeRig(t)
	id := rig.identity(t, rig.signIn(t, "tok-alice"))
	if st := rig.f.Status(context.Background(), id); !st.Reachable {
		t.Fatalf("a forge that answers reads unreachable: %+v", st)
	}
	rig.f.noteFailure(errors.New("dial tcp: connection refused"))
	if rig.f.Status(context.Background(), id).Reachable {
		t.Fatal("a failed forge call did not hold the automatic connect back")
	}
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	cfg := rig.f.cfg
	cfg.APIURL = down.URL
	if NewForge(cfg, rig.a).Status(context.Background(), id).Reachable {
		t.Fatal("a forge that does not answer reads reachable")
	}
	// A refusal the forge answered is no outage.
	rig.f.probeMu.Lock()
	rig.f.failedAt = time.Time{}
	rig.f.probeMu.Unlock()
	rig.f.noteFailure(&oauth2.RetrieveError{Response: &http.Response{StatusCode: 400}, ErrorCode: "invalid_grant"})
	rig.f.noteFailure(fmt.Errorf("exchange: %w", context.Canceled))
	if !rig.f.Status(context.Background(), id).Reachable {
		t.Fatal("a forge's own refusal, or a caller hanging up, read as an outage")
	}
}

// A caller hanging up mid-probe does not record the forge as down for
// everyone: the probe runs detached.
func TestForgeProbeOutlivesItsCaller(t *testing.T) {
	rig := newForgeRig(t)
	id := rig.identity(t, rig.signIn(t, "tok-alice"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rig.f.Status(ctx, id)
	if !rig.f.Status(context.Background(), id).Reachable {
		t.Fatal("a cancelled caller's probe cached the forge as down")
	}
}

// A refusal answered at the account lookup (a 401) is no outage.
func TestForgeAccountRefusalIsNoOutage(t *testing.T) {
	rig := newForgeRig(t)
	if _, err := rig.f.account(context.Background(), "not-a-token"); err == nil || errors.Is(err, errForgeUnreachable) {
		t.Fatalf("a 401 from the forge: %v, want a refusal, not an outage", err)
	}
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	f := NewForge(ForgeConfig{APIURL: down.URL}, rig.a)
	if _, err := f.account(context.Background(), "x"); !errors.Is(err, errForgeUnreachable) {
		t.Fatalf("a forge that does not answer: %v, want an outage", err)
	}
}
