package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
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

	"github.com/epheo/dotvirt/internal/restfactory"
)

// sessionRig serves an authenticated REST route, the stream route and logout
// behind the middleware, and records what logout reported.
type sessionRig struct {
	a        *Authenticator
	h        http.Handler
	reviews  *int
	ended    []bool
	streamed int
}

func newSessionRig(t *testing.T) *sessionRig {
	t.Helper()
	a, reviews := fakeAuth(map[string]authnv1.UserInfo{"good": {Username: "alice"}})
	rig := &sessionRig{a: a, reviews: reviews}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/inventory", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /api/inventory/stream", func(http.ResponseWriter, *http.Request) { rig.streamed++ })
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		_, ended := a.Logout(w, r)
		rig.ended = append(rig.ended, ended)
	})
	rig.h = a.Middleware(mux)
	return rig
}

func (rig *sessionRig) login(t *testing.T) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	rig.a.Login(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"good"}`)))
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	t.Fatalf("login set no session cookie: %d %s", rec.Code, rec.Body)
	return nil
}

func sealedAt(issued time.Time) *http.Cookie {
	return &http.Cookie{Name: cookieName, Value: cookieValue(session{token: "good", issued: issued}, testSealer)}
}

// do sends one request carrying c (or the bearer token when c is nil).
func (rig *sessionRig) do(method, path string, c *http.Cookie) int {
	return rig.send(method, path, c).Code
}

func (rig *sessionRig) send(method, path string, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if c != nil {
		r.AddCookie(c)
	} else {
		r.Header.Set("Authorization", "Bearer good")
	}
	if strings.HasSuffix(path, "/stream") {
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
	}
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, r)
	return rec
}

// clears reports whether rec expires the session cookie.
func clears(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && c.MaxAge < 0 {
			return true
		}
	}
	return false
}

// withLegacyUntil moves the legacy deadline for one test, so no test depends
// on today's date.
func withLegacyUntil(t *testing.T, until time.Time) {
	t.Helper()
	saved := legacyUntil
	legacyUntil = until
	t.Cleanup(func() { legacyUntil = saved })
}

func legacyCookie() *http.Cookie {
	sealed := testSealer.Seal(nil, nil, []byte("good"), []byte(legacyPrefix))
	return &http.Cookie{Name: cookieName, Value: legacyPrefix + base64.RawURLEncoding.EncodeToString(sealed)}
}

// A logout ends its session server-side: the same cookie is refused on REST
// and on the stream upgrade, ahead of the verdict cache (the token itself still
// validates, and no TokenReview is spent on the refusal), while a fresh sign-in
// with the same token works.
func TestLogoutEndsSession(t *testing.T) {
	rig := newSessionRig(t)
	c := rig.login(t)
	if code := rig.do(http.MethodGet, "/api/inventory", c); code != http.StatusOK {
		t.Fatalf("live session: %d", code)
	}
	if code := rig.do(http.MethodPost, "/api/logout", c); code != http.StatusNoContent {
		t.Fatalf("logout: %d, want 204", code)
	}
	if len(rig.ended) != 1 || !rig.ended[0] {
		t.Fatalf("logout reported %v, want one ended session", rig.ended)
	}
	if code := rig.do(http.MethodGet, "/api/inventory", c); code != http.StatusUnauthorized {
		t.Errorf("signed-out cookie on REST: %d, want 401", code)
	}
	if code := rig.do(http.MethodGet, "/api/inventory/stream", c); code != http.StatusUnauthorized || rig.streamed != 0 {
		t.Errorf("signed-out cookie on the stream upgrade: %d (reached %d), want 401", code, rig.streamed)
	}
	if rec := rig.send(http.MethodPost, "/api/logout", c); rec.Code != http.StatusNoContent || !clears(rec) || rig.ended[1] {
		t.Errorf("signed-out cookie logging out again: %d (clears %v, ended %v), want a 204 that clears and ends nothing", rec.Code, clears(rec), rig.ended[1])
	}
	if *rig.reviews != 1 {
		t.Errorf("TokenReviews = %d, want 1: an ended session is refused before validation", *rig.reviews)
	}
	if code := rig.do(http.MethodGet, "/api/inventory", nil); code != http.StatusOK {
		t.Errorf("bearer with the same token: %d; logout ends sessions, not the token", code)
	}
	if code := rig.do(http.MethodGet, "/api/inventory", rig.login(t)); code != http.StatusOK {
		t.Errorf("fresh sign-in after logout: %d", code)
	}
}

// A logout ends its session and every older one of the token, never a newer
// one; a dead cookie logging out again cannot reach a newer session.
func TestLogoutEndsOlderSessionsOnly(t *testing.T) {
	rig := newSessionRig(t)
	now := time.Now()
	old, mid, newer := sealedAt(now.Add(-2*time.Hour)), sealedAt(now.Add(-time.Hour)), sealedAt(now.Add(-time.Minute))
	if code := rig.do(http.MethodPost, "/api/logout", mid); code != http.StatusNoContent {
		t.Fatalf("logout: %d", code)
	}
	for name, c := range map[string]*http.Cookie{"older": old, "signed-out": mid} {
		if code := rig.do(http.MethodGet, "/api/inventory", c); code != http.StatusUnauthorized {
			t.Errorf("%s session: %d, want 401", name, code)
		}
	}
	if code := rig.do(http.MethodPost, "/api/logout", old); code != http.StatusNoContent || rig.ended[1] {
		t.Errorf("dead cookie logging out: %d (ended %v), want 204 ending nothing", code, rig.ended[1])
	}
	if code := rig.do(http.MethodGet, "/api/inventory", newer); code != http.StatusOK {
		t.Errorf("newer session: %d, want 200", code)
	}
}

// A bearer header has no session: its logout ends nothing, neither the token
// nor a cookie session of it.
func TestBearerLogoutEndsNothing(t *testing.T) {
	rig := newSessionRig(t)
	c := rig.login(t)
	if code := rig.do(http.MethodPost, "/api/logout", nil); code != http.StatusNoContent {
		t.Fatalf("bearer logout: %d, want 204", code)
	}
	if len(rig.ended) != 1 || rig.ended[0] {
		t.Errorf("bearer logout reported %v, want nothing ended", rig.ended)
	}
	if code := rig.do(http.MethodGet, "/api/inventory", nil); code != http.StatusOK {
		t.Errorf("bearer after its logout: %d", code)
	}
	if code := rig.do(http.MethodGet, "/api/inventory", c); code != http.StatusOK {
		t.Errorf("cookie session after a bearer logout: %d", code)
	}
}

// A session ends at sessionMaxAge even when its token still validates.
func TestSessionLifetime(t *testing.T) {
	rig := newSessionRig(t)
	now := time.Now()
	if code := rig.do(http.MethodGet, "/api/inventory", sealedAt(now.Add(-sessionMaxAge+time.Minute))); code != http.StatusOK {
		t.Errorf("session within its lifetime: %d", code)
	}
	if code := rig.do(http.MethodGet, "/api/inventory", sealedAt(now.Add(-sessionMaxAge-time.Minute))); code != http.StatusUnauthorized {
		t.Errorf("session past its lifetime: %d, want 401", code)
	}
}

// A cookie sealed before issue times still opens after the upgrade, older than
// every session: any logout of its token ends it, a logout of it ends no newer
// session, and it reads as logged out from legacyUntil.
func TestLegacyCookie(t *testing.T) {
	withLegacyUntil(t, time.Now().Add(time.Hour))
	rig := newSessionRig(t)
	c := legacyCookie()
	if s, ok := parseCookieValue(c.Value, testSealer); !ok || s.token != "good" || !s.issued.Equal(legacyIssued) || s.sso {
		t.Fatalf("legacy cookie opened as %+v (%v), want the token at the epoch, not SSO", s, ok)
	}
	if code := rig.do(http.MethodGet, "/api/inventory", c); code != http.StatusOK {
		t.Fatalf("legacy session: %d, want 200", code)
	}

	newer := rig.login(t)
	rig.do(http.MethodPost, "/api/logout", c)
	if code := rig.do(http.MethodGet, "/api/inventory", c); code != http.StatusUnauthorized {
		t.Errorf("signed-out legacy session: %d, want 401", code)
	}
	if code := rig.do(http.MethodGet, "/api/inventory", newer); code != http.StatusOK {
		t.Errorf("a legacy logout ended a newer session: %d", code)
	}

	other := newSessionRig(t)
	other.do(http.MethodPost, "/api/logout", sealedAt(time.Now().Add(-2*time.Hour)))
	if code := other.do(http.MethodGet, "/api/inventory", c); code != http.StatusUnauthorized {
		t.Errorf("a logout of an older-process v3 session left the legacy cookie: %d, want 401", code)
	}

	withLegacyUntil(t, time.Now())
	if code := newSessionRig(t).do(http.MethodGet, "/api/inventory", c); code != http.StatusUnauthorized {
		t.Errorf("legacy cookie past legacyUntil: %d, want 401", code)
	}
}

// The record keeps an entry only until the session it names expires: a v3
// one for sessionMaxAge, a legacy one until legacyUntil. It sweeps at most once
// per sweepEvery.
func TestEndedEntriesExpire(t *testing.T) {
	now := time.Now()
	withLegacyUntil(t, now.Add(23*24*time.Hour))
	s := newSessions()
	s.end("stale", now.Add(-sessionMaxAge-time.Second))
	s.end("legacy", legacyIssued)
	s.swept = now.Add(-sweepEvery)
	s.end("fresh", now)
	if _, ok := s.ended["stale"]; ok {
		t.Error("kept a v3 entry past its own expiry until legacyUntil")
	}
	if _, ok := s.ended["legacy"]; !ok {
		t.Error("dropped a legacy entry before legacyUntil")
	}

	s.end("stale", now.Add(-sessionMaxAge-time.Second))
	s.end("fresh", now.Add(-time.Hour)) // an older session never lowers the mark
	if _, ok := s.ended["stale"]; !ok {
		t.Fatal("swept again inside sweepEvery")
	}
	if !s.ended["fresh"].Equal(now) {
		t.Fatalf("fresh mark = %v, want its newest issue time", s.ended["fresh"])
	}
	if s.live("fresh", now) || !s.live("fresh", now.Add(time.Nanosecond)) {
		t.Error("the mark must end its own session and spare a newer one")
	}
}

// Past its cap the record drops the entry closest to expiring on its own, so a
// flood of logouts can neither grow it without bound nor evict a legacy entry
// before a v3 one.
func TestEndedRecordCap(t *testing.T) {
	now := time.Now()
	withLegacyUntil(t, now.Add(23*24*time.Hour))
	s := newSessions()
	s.limit = 3
	s.end("legacy", legacyIssued)
	s.end("older", now.Add(-2*time.Hour))
	s.end("newer", now.Add(-time.Hour))
	s.end("newest", now)
	if len(s.ended) != 3 {
		t.Fatalf("record holds %d entries, want the cap of 3", len(s.ended))
	}
	if _, ok := s.ended["older"]; ok {
		t.Error("kept the entry closest to expiry past the cap")
	}
	if _, ok := s.ended["legacy"]; !ok {
		t.Error("evicted the legacy entry, which expires last")
	}
	s.end("newest", now.Add(time.Second)) // a known token moves its mark without evicting
	if len(s.ended) != 3 {
		t.Errorf("updating a known token evicted: %d entries", len(s.ended))
	}
}

// v010StateCookie is a v0.1.0 OAuth state cookie, harvestable from the open
// sign-in redirect: sealed like a v0.1.0 session cookie, so it opens as a
// legacy session whose token is the random state.
func v010StateCookie(t *testing.T) *http.Cookie {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	sealed := testSealer.Seal(nil, nil, []byte(base64.RawURLEncoding.EncodeToString(b)), []byte(legacyPrefix))
	return &http.Cookie{Name: cookieName, Value: legacyPrefix + base64.RawURLEncoding.EncodeToString(sealed)}
}

// A legacy cookie's logout counts only once its token validates, on the
// sign-in budget: replayed harvested state cookies get their 204 and a cleared
// cookie but grow no record and cost at most the budget in reviews, while a
// real legacy session still logs out.
func TestHarvestedLegacyStateEndsNothing(t *testing.T) {
	withLegacyUntil(t, time.Now().Add(time.Hour))
	rig := newSessionRig(t)
	rig.a.reviews = rate.NewLimiter(0, 3)
	for range 50 {
		if rec := rig.send(http.MethodPost, "/api/logout", v010StateCookie(t)); rec.Code != http.StatusNoContent || !clears(rec) {
			t.Fatalf("harvested state logout: %d (clears %v), want a 204 that clears", rec.Code, clears(rec))
		}
	}
	for _, ended := range rig.ended {
		if ended {
			t.Fatal("a harvested state cookie ended a session")
		}
	}
	if n := len(rig.a.sessions.ended); n != 0 {
		t.Errorf("harvested state cookies left %d logout records", n)
	}
	if *rig.reviews > 3 {
		t.Errorf("harvested state cookies spent %d TokenReviews, past the budget of 3", *rig.reviews)
	}

	rig.a.reviews = rate.NewLimiter(rate.Inf, 0)
	c := legacyCookie()
	if rig.do(http.MethodPost, "/api/logout", c); !rig.ended[len(rig.ended)-1] {
		t.Fatal("a real legacy session's logout ended nothing")
	}
	if code := rig.do(http.MethodGet, "/api/inventory", c); code != http.StatusUnauthorized {
		t.Errorf("signed-out legacy session: %d, want 401", code)
	}
}

// Logout needs no TokenReview: with the sign-in budget spent or the apiserver
// down, it still ends the session and clears the cookie.
func TestLogoutWithoutTokenReview(t *testing.T) {
	for _, c := range []struct {
		name  string
		block func(a *Authenticator) (restore func())
	}{
		{"budget spent", func(a *Authenticator) func() {
			saved := a.reviews
			a.reviews = rate.NewLimiter(0, 0)
			return func() { a.reviews = saved }
		}},
		{"apiserver down", func(a *Authenticator) func() {
			kube := a.saKube.(*fake.Clientset)
			down := true
			kube.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
				if down {
					return true, nil, errors.New("connection refused")
				}
				return false, nil, nil
			})
			return func() { down = false }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newSessionRig(t)
			cookie := rig.login(t)
			rig.a.cache.Delete(restfactory.TokenKey("good")) // the verdict aged out
			restore := c.block(rig.a)
			rec := rig.send(http.MethodPost, "/api/logout", cookie)
			restore()
			if rec.Code != http.StatusNoContent || !clears(rec) || len(rig.ended) != 1 || !rig.ended[0] {
				t.Fatalf("logout: %d (clears %v, ended %v), want a 204 that clears and ends the session", rec.Code, clears(rec), rig.ended)
			}
			if code := rig.do(http.MethodGet, "/api/inventory", cookie); code != http.StatusUnauthorized {
				t.Errorf("session after its logout: %d, want 401", code)
			}
		})
	}
}

// A 401 for a session that is over also expires its cookie; a refused bearer
// header leaves cookies alone.
func TestSessionEndClearsCookie(t *testing.T) {
	valid := map[string]authnv1.UserInfo{"good": {Username: "alice"}}
	a, _ := fakeAuth(valid)
	rig := &sessionRig{a: a, h: a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))}
	ended := sealedAt(time.Now().Add(-time.Hour))
	a.sessions.end(restfactory.TokenKey("good"), time.Now().Add(-time.Hour))
	unreadable := &http.Cookie{Name: cookieName, Value: "v3.garbage"}
	for name, c := range map[string]*http.Cookie{"ended": ended, "expired": sealedAt(time.Now().Add(-sessionMaxAge)), "unreadable": unreadable} {
		if rec := rig.send(http.MethodGet, "/api/inventory", c); rec.Code != http.StatusUnauthorized || !clears(rec) {
			t.Errorf("%s cookie: %d (clears %v), want a 401 that clears", name, rec.Code, clears(rec))
		}
	}
	delete(valid, "good")
	a.cache.Delete(restfactory.TokenKey("good"))
	if rec := rig.send(http.MethodGet, "/api/inventory", sealedAt(time.Now())); rec.Code != http.StatusUnauthorized || !clears(rec) {
		t.Errorf("rejected token's cookie: %d (clears %v), want a 401 that clears", rec.Code, clears(rec))
	}
	if rec := rig.send(http.MethodGet, "/api/inventory", nil); rec.Code != http.StatusUnauthorized || clears(rec) {
		t.Errorf("rejected bearer: %d (clears %v), want a 401 that leaves cookies alone", rec.Code, clears(rec))
	}
}

// A value sealed for one cookie never opens as the other.
func TestStateAndSessionCookiesDoNotCross(t *testing.T) {
	if _, ok := parseCookieValue(stateValue("state", testSealer), testSealer); ok {
		t.Error("a state cookie opened as a session")
	}
	if _, ok := parseStateValue(cookieValue(session{token: "good", issued: time.Now()}, testSealer), testSealer); ok {
		t.Error("a session cookie opened as a state")
	}
	if got, ok := parseStateValue(stateValue("state", testSealer), testSealer); !ok || got != "state" {
		t.Errorf("state round-trip = %q, %v", got, ok)
	}
}

// Concurrent logouts and checks of one token converge on the newest mark (run
// under -race).
func TestSessionsConcurrent(t *testing.T) {
	s := newSessions()
	base := time.Now().Add(-time.Hour)
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.end("tok", base.Add(time.Duration(i)*time.Second))
		}()
		go func() {
			defer wg.Done()
			s.live("tok", base.Add(time.Duration(i)*time.Second))
		}()
	}
	wg.Wait()
	if want := base.Add(63 * time.Second); !s.ended["tok"].Equal(want) {
		t.Fatalf("mark = %v, want the newest ended session %v", s.ended["tok"], want)
	}
}

// The middleware hands the session's origin and issue time to the handler: an
// SSO cookie is marked, a pasted sign-in and a bearer header are not.
func TestSessionOriginReachesIdentity(t *testing.T) {
	a, _ := fakeAuth(map[string]authnv1.UserInfo{"good": {Username: "alice"}})
	var seen Identity
	h := a.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen, _ = FromContext(r.Context()) }))
	rig := &sessionRig{a: a, h: h}
	sso := &http.Cookie{Name: cookieName, Value: cookieValue(session{token: "good", issued: time.Now(), sso: true}, testSealer)}
	for _, c := range []struct {
		name       string
		cookie     *http.Cookie
		sso, issue bool
	}{
		{"pasted sign-in", rig.login(t), false, true},
		{"sso", sso, true, true},
		{"bearer", nil, false, false},
	} {
		seen = Identity{}
		rig.do(http.MethodGet, "/api/inventory", c.cookie)
		if seen.Username != "alice" || seen.SSO != c.sso || seen.Issued.IsZero() == c.issue {
			t.Errorf("%s: identity %+v, want SSO %v and an issue time %v", c.name, seen, c.sso, c.issue)
		}
	}
}

// A token a TokenReview rejected ends every session of it, bearer included,
// so its open streams and consoles close on the next sweep.
func TestRejectedTokenEndsSessions(t *testing.T) {
	valid := map[string]authnv1.UserInfo{"good": {Username: "alice"}}
	a, _ := fakeAuth(valid)
	key, now := restfactory.TokenKey("good"), time.Now()
	if _, err := a.Validate(context.Background(), "good"); err != nil || !a.SessionLive(key, now) {
		t.Fatalf("live token: err %v, live %v", err, a.SessionLive(key, now))
	}
	delete(valid, "good")
	if err := a.Recheck(context.Background(), "good"); !errors.Is(err, ErrRejected) {
		t.Fatalf("recheck: %v, want a rejection", err)
	}
	if a.SessionLive(key, now) || a.SessionLive(key, time.Time{}) {
		t.Error("a rejected token's session or bearer use is still live")
	}
}

// The object name is the oauth-apiserver's: sha256~ plus the unpadded base64url
// SHA-256 of the token after its prefix (sha256("abc") is the FIPS 180-2
// vector). A token without the prefix is no OAuth access token.
func TestOAuthTokenName(t *testing.T) {
	if name, ok := OAuthTokenName("sha256~abc"); !ok || name != "sha256~ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0" {
		t.Errorf("OAuthTokenName = %q, %v", name, ok)
	}
	if _, ok := OAuthTokenName("eyJhbGciOi.sa-jwt"); ok {
		t.Error("named an object for a token without the sha256~ prefix")
	}
}

// A stream's re-check fails for an ended session before any review, so its
// frames stop through the build path as well as the sweep; a newer session and
// a bearer use of the same token still pass.
func TestRevalidateRejectsEndedSession(t *testing.T) {
	rig := newSessionRig(t)
	now := time.Now()
	ended, newer := now.Add(-time.Hour), now.Add(-time.Minute)
	rig.do(http.MethodPost, "/api/logout", sealedAt(ended))
	reviews := *rig.reviews
	ctx := context.Background()
	for name, issued := range map[string]time.Time{"signed-out": ended, "expired": now.Add(-sessionMaxAge)} {
		if err := rig.a.Revalidate(ctx, Identity{Token: "good", Issued: issued}); !errors.Is(err, ErrSessionEnded) || !errors.Is(err, ErrRejected) {
			t.Errorf("%s session re-checked: %v, want ErrSessionEnded", name, err)
		}
	}
	if *rig.reviews != reviews {
		t.Errorf("an ended session's re-check spent %d TokenReviews", *rig.reviews-reviews)
	}
	for name, issued := range map[string]time.Time{"newer": newer, "bearer": {}} {
		if err := rig.a.Revalidate(ctx, Identity{Token: "good", Issued: issued}); err != nil {
			t.Errorf("%s re-checked: %v, want nil", name, err)
		}
	}
}
