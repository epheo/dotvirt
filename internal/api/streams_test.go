package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
)

// A cached view or verdict serves while younger than authTTL, and only
// a backstop rebuild re-evaluates it once it ages out. In each mode, wherever
// the entry was cached between two publishes, the first publish that finds it
// expired must come within the documented bound; a period of the TTL itself
// would let it wait a whole second TTL.
func TestTTLBackstopBound(t *testing.T) {
	for _, mode := range []struct {
		ttl, bound time.Duration
	}{{visibleTTL, 7*time.Minute + 30*time.Second}, {unwatchedTTL, 45 * time.Second}} {
		if got := backstopBound(mode.ttl); got != mode.bound {
			t.Fatalf("%s entries: documented bound %s, computed %s", mode.ttl, mode.bound, got)
		}
		// Publishes in this mode, on the tick grid.
		var publishes []time.Duration
		for ticks, at := 0, time.Duration(0); at < 3*mode.ttl; at += backstopTick {
			if ticks++; backstopDue(ticks, mode.ttl) {
				ticks = 0
				publishes = append(publishes, at+backstopTick)
			}
		}
		for cached := time.Duration(0); cached < backstopEvery(mode.ttl); cached += 100 * time.Millisecond {
			i := 0
			for publishes[i]-cached < mode.ttl {
				i++
			}
			if lag := publishes[i] - cached; lag > mode.bound {
				t.Fatalf("%s entries: one cached %s after a publish is re-evaluated %s later, past the %s bound", mode.ttl, cached, lag, mode.bound)
			}
		}
	}
}

// An entry reviewed right after a grant move serves the short TTL even while
// every watch runs, so the backstop keeps the short period until no such
// entry can still be serving: its expiry is noticed within seconds, not at
// the next long tick.
func TestTTLBackstopShortWhileReviewsSettle(t *testing.T) {
	bus := eventbus.New()
	st, _, _ := watchedState(t, bus) // its initial lists are grant moves
	s := NewServer(Deps{State: st, Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}})
	if s.authTTL() != visibleTTL {
		t.Fatal("every watch runs, yet entries do not serve the long TTL")
	}
	if got := s.backstopTTL(); got != unwatchedTTL {
		t.Errorf("right after a grant move the backstop follows %s, want the short %s", got, unwatchedTTL)
	}
	last := grantSettle + unwatchedTTL // the newest unsettled entry expires here
	if !unsettledMayServe(last) || unsettledMayServe(last+backstopTick) {
		t.Error("the short period does not end one tick after the last unsettled entry expires")
	}
}

// A watch failing puts the backstop on the short period at the next tick, so
// a revocation the watches miss waits seconds, not the rest of a long period.
func TestTTLBackstopSwitchesAtTheNextTick(t *testing.T) {
	ticks := 0
	for range 4 {
		if ticks++; backstopDue(ticks, visibleTTL) {
			t.Fatal("published early on the long period")
		}
	}
	if ticks++; !backstopDue(ticks, unwatchedTTL) {
		t.Fatal("the switch to the short TTL waited past its next tick")
	}
	if !backstopDue(10, visibleTTL) || backstopDue(9, visibleTTL) {
		t.Fatal("the long period is not ten ticks")
	}
}

// Visibility and SSAR verdicts also move with authorization no watch
// observes; they are cached for visibleTTL only, and an expired entry is
// re-evaluated only by a rebuild. Frames rebuild only on bus events, so the
// backstop must publish while a stream is open, never when none is, and never
// through a kind the RBAC-stamped caches read.
func TestTTLBackstopWakesOnlyOpenStreams(t *testing.T) {
	bus := eventbus.New()
	fd := &fakeDraft{prs: map[string]model.Proposal{}}
	s := NewServer(Deps{Reader: fd, Draft: fd, Bus: bus})

	s.ttlBackstop()
	if v := bus.Version(eventbus.TTLBackstop); v != 0 {
		t.Fatalf("published %d times with no stream open", v)
	}

	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	stream := s.streaming(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	})
	req := httptest.NewRequest(http.MethodGet, "/api/inventory/stream", nil)
	req = req.WithContext(auth.NewContext(req.Context(), auth.Identity{Token: "tok", Username: "u"}))
	go func() {
		stream(httptest.NewRecorder(), req)
		close(done)
	}()
	<-entered

	lv := s.visLevels()
	s.ttlBackstop()
	if v := bus.Version(eventbus.TTLBackstop); v != 1 {
		t.Fatalf("published %d times with a stream open, want 1", v)
	}
	if s.visLevels() != lv {
		t.Fatal("the backstop invalidated the RBAC-stamped caches")
	}

	close(release)
	<-done
	s.ttlBackstop()
	if v := bus.Version(eventbus.TTLBackstop); v != 1 {
		t.Fatalf("published after the last stream closed: %d", v)
	}
}
