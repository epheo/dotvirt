package auth

import (
	"sync"
	"time"
)

// sessionMaxAge bounds a session's life whatever its token's, so a captured
// cookie dies with it and a logout record is kept no longer. It is OpenShift's
// default access token lifetime: an SSO session already ends on its token.
const sessionMaxAge = 24 * time.Hour

// sweepEvery spaces the logout record's sweeps: each scans the whole map under
// the lock every request takes, so a logout loop must not run one per call.
const sweepEvery = time.Hour

// maxEnded backstops the record's size, far above one entry per token signed
// out in a day: past it, the entry closest to expiring on its own is dropped.
const maxEnded = 100_000

// SessionExpiry is when a session issued at issued stops authenticating; zero
// for a bearer header, which has no session.
func SessionExpiry(issued time.Time) time.Time {
	switch {
	case issued.IsZero():
		return time.Time{}
	case issued.Equal(legacyIssued):
		return legacyUntil
	}
	return issued.Add(sessionMaxAge)
}

// sessions is the logout record. A logout ends its session and every older one
// of the same token, so one entry per token holds the newest issue time a logout
// ended: a login/logout loop cannot grow the map, and a dead cookie replayed at
// logout cannot end a newer session. An entry is dropped once the session it
// names has expired, so a v3 entry lasts sessionMaxAge and a legacy one until
// legacyUntil; a legacy cookie a v3 logout of its token ended works again once
// that entry goes, until legacyUntil.
//
// It lives in memory (dotvirt runs one replica), so a restart forgets it: a
// signed-out cookie whose token still validates works again until it expires.
type sessions struct {
	mu    sync.Mutex
	ended map[string]time.Time // token key -> newest issue time a logout ended
	swept time.Time
	limit int // maxEnded; tests shrink it
}

func newSessions() *sessions {
	return &sessions{ended: map[string]time.Time{}, limit: maxEnded}
}

// end records the logout of the session issued at issued for tokenKey.
func (s *sessions) end(tokenKey string, issued time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.swept) >= sweepEvery {
		for k, t := range s.ended {
			if !now.Before(SessionExpiry(t)) {
				delete(s.ended, k)
			}
		}
		s.swept = now
	}
	prev, ok := s.ended[tokenKey]
	if !issued.After(prev) {
		return
	}
	if !ok && len(s.ended) >= s.limit {
		s.evictLocked()
	}
	s.ended[tokenKey] = issued
}

// evictLocked drops the entry closest to expiring on its own.
func (s *sessions) evictLocked() {
	var first string
	var at time.Time
	for k, t := range s.ended {
		if e := SessionExpiry(t); first == "" || e.Before(at) {
			first, at = k, e
		}
	}
	delete(s.ended, first)
}

// live reports whether the session issued at issued for tokenKey may still
// authenticate. A zero issued is a bearer header: it has no session to end.
func (s *sessions) live(tokenKey string, issued time.Time) bool {
	if issued.IsZero() {
		return true
	}
	if !time.Now().Before(SessionExpiry(issued)) {
		return false
	}
	s.mu.Lock()
	ended, ok := s.ended[tokenKey]
	s.mu.Unlock()
	return !ok || issued.After(ended)
}
