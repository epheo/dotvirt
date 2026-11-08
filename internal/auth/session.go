package auth

import (
	"sync"
	"time"
)

// sessionMaxAge bounds a session's life whatever its token's, so a captured
// cookie dies with it and a logout record is kept no longer. It is OpenShift's
// default access token lifetime: an SSO session already ends on its token.
const sessionMaxAge = 24 * time.Hour

// sessions is the logout record. A logout ends its session and every older one
// of the same token, so one entry per token holds the newest issue time a logout
// ended: a login/logout loop cannot grow the map, and a dead cookie replayed at
// logout cannot end a newer session. An entry is dropped once every session it
// ends is past sessionMaxAge.
//
// It lives in memory (dotvirt runs one replica), so a restart forgets it: a
// signed-out cookie whose token still validates works again until its
// sessionMaxAge.
type sessions struct {
	mu    sync.Mutex
	ended map[string]time.Time // token key -> newest issue time a logout ended
}

func newSessions() *sessions {
	return &sessions{ended: map[string]time.Time{}}
}

// end records the logout of the session issued at issued for tokenKey.
func (s *sessions) end(tokenKey string, issued time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Logouts are rare and the map holds one entry per token, so each one sweeps.
	now := time.Now()
	for k, t := range s.ended {
		if now.Sub(t) >= sessionMaxAge {
			delete(s.ended, k)
		}
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
	if time.Since(issued) >= sessionMaxAge {
		return false
	}
	s.mu.Lock()
	ended, ok := s.ended[tokenKey]
	s.mu.Unlock()
	return !ok || issued.After(ended)
}
