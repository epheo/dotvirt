package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"

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
		rig.ended = append(rig.ended, a.Logout(w, r))
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
	return &http.Cookie{Name: cookieName, Value: cookieValue("good", issued, testSealer)}
}

// do sends one request carrying c (or the bearer token when c is nil).
func (rig *sessionRig) do(method, path string, c *http.Cookie) int {
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
	return rec.Code
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
	if code := rig.do(http.MethodPost, "/api/logout", c); code != http.StatusUnauthorized {
		t.Errorf("signed-out cookie logging out again: %d, want 401", code)
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
// one; a dead cookie cannot log out again to reach a newer session.
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
	if code := rig.do(http.MethodPost, "/api/logout", old); code != http.StatusUnauthorized {
		t.Errorf("dead cookie logging out: %d, want 401", code)
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

// A cookie sealed before issue times still opens after the upgrade, reads as
// issued at process start, and a logout ends it.
func TestLegacyCookieSurvivesUpgrade(t *testing.T) {
	rig := newSessionRig(t)
	sealed := testSealer.Seal(nil, nil, []byte("good"), []byte(legacyPrefix))
	c := &http.Cookie{Name: cookieName, Value: legacyPrefix + base64.RawURLEncoding.EncodeToString(sealed)}
	if tok, issued, ok := parseCookieValue(c.Value, testSealer); !ok || tok != "good" || !issued.Equal(legacyIssued) {
		t.Fatalf("legacy cookie opened as %q at %v (%v), want the token at process start", tok, issued, ok)
	}
	if code := rig.do(http.MethodGet, "/api/inventory", c); code != http.StatusOK {
		t.Fatalf("legacy session: %d, want 200", code)
	}
	rig.do(http.MethodPost, "/api/logout", c)
	if code := rig.do(http.MethodGet, "/api/inventory", c); code != http.StatusUnauthorized {
		t.Errorf("signed-out legacy session: %d, want 401", code)
	}
}

// The record keeps an entry only while a session it ends could still be live.
func TestEndedEntriesExpire(t *testing.T) {
	s := newSessions()
	now := time.Now()
	s.end("stale", now.Add(-sessionMaxAge-time.Second))
	s.end("fresh", now)
	s.end("fresh", now.Add(-time.Hour)) // an older session never lowers the mark
	if len(s.ended) != 1 || !s.ended["fresh"].Equal(now) {
		t.Fatalf("record = %v, want only fresh at its newest issue time", s.ended)
	}
	if s.live("fresh", now) || !s.live("fresh", now.Add(time.Nanosecond)) {
		t.Error("the mark must end its own session and spare a newer one")
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
