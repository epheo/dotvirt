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
// logout cannot end a newer session. An entry is dropped once every session it
// ends has expired, legacy sessions of its token included.
//
// It lives in memory (dotvirt runs one replica), so a restart forgets it: a
// signed-out cookie whose token still validates works again until it expires.
type sessions struct {
	mu    sync.Mutex
	ended map[string]time.Time // token key -> newest issue time a logout ended
	swept time.Time
}

func newSessions() *sessions {
	return &sessions{ended: map[string]time.Time{}}
}

// end records the logout of the session issued at issued for tokenKey.
func (s *sessions) end(tokenKey string, issued time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.swept) >= sweepEvery {
		for k, t := range s.ended {
			if !now.Before(SessionExpiry(t)) && !now.Before(legacyUntil) {
				delete(s.ended, k)
			}
		}
		s.swept = now
	}
	if issued.After(s.ended[tokenKey]) {
		s.ended[tokenKey] = issued
	}
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
