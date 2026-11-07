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

// A cached visible set or verdict serves while younger than visibleTTL
// (ttlcache: age < ttl is a hit), and only a backstop rebuild re-evaluates it
// once it ages out. Wherever the entry was cached between two ticks, the first
// tick that finds it expired must come within ttlBackstopBound; ticking at
// visibleTTL itself would let it wait a whole second TTL.
func TestTTLBackstopBound(t *testing.T) {
	if ttlBackstopBound != 7*time.Minute+30*time.Second {
		t.Fatalf("documented bound is 7.5 minutes, constant says %s", ttlBackstopBound)
	}
	for cached := time.Duration(0); cached < ttlBackstopEvery; cached += 100 * time.Millisecond {
		tick := time.Duration(0)
		for tick-cached < visibleTTL {
			tick += ttlBackstopEvery
		}
		if lag := tick - cached; lag > ttlBackstopBound {
			t.Fatalf("an entry cached %s after a tick is re-evaluated %s later, past the %s bound", cached, lag, ttlBackstopBound)
		}
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

	rbac := s.rbacVersion()
	s.ttlBackstop()
	if v := bus.Version(eventbus.TTLBackstop); v != 1 {
		t.Fatalf("published %d times with a stream open, want 1", v)
	}
	if s.rbacVersion() != rbac {
		t.Fatal("the backstop invalidated the RBAC-stamped caches")
	}

	close(release)
	<-done
	s.ttlBackstop()
	if v := bus.Version(eventbus.TTLBackstop); v != 1 {
		t.Fatalf("published after the last stream closed: %d", v)
	}
}
