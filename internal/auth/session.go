package auth

import (
	"log"
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

// evictReportEvery spaces the log lines reporting cap evictions, so filling the
// record is visible without flooding the log.
const evictReportEvery = time.Minute

// sessions is the logout record. A logout ends its session and every older one
// of the same token, so one entry per token holds the newest issue time a logout
// ended: a login/logout loop cannot grow the map, and a dead cookie replayed at
// logout cannot end a newer session. An entry is dropped once every session it
// ends has expired: a v3 mark after sessionMaxAge, a legacy session's own logout
// at legacyUntil. A legacy cookie that only a v3 logout of its token ended works
// again once that mark expires, until legacyUntil.
//
// It lives in memory (dotvirt runs one replica), so a restart forgets it: a
// signed-out cookie whose token still validates works again until it expires.
type sessions struct {
	mu    sync.Mutex
	ended map[string]mark // by token key
	swept time.Time
	limit int // maxEnded; tests shrink it

	evicted  int // since the last report
	reported time.Time
}

// mark is one token's logouts.
type mark struct {
	issued time.Time // the newest v3 issue time a logout ended
	// legacy records a legacy session's own logout, which no v3 mark replaces:
	// it outlives every v3 one.
	legacy bool
}

// expires is when every session m ends has expired.
func (m mark) expires() time.Time {
	var at time.Time
	if !m.issued.IsZero() {
		at = SessionExpiry(m.issued)
	}
	if m.legacy && legacyUntil.After(at) {
		at = legacyUntil
	}
	return at
}

func newSessions() *sessions {
	return &sessions{ended: map[string]mark{}, limit: maxEnded}
}

// end records the logout of the session issued at issued for tokenKey.
func (s *sessions) end(tokenKey string, issued time.Time) {
	if n, ok := s.record(tokenKey, issued); ok {
		log.Printf("auth: logout record at its cap of %d: evicted %d entries since the last report; the sessions they ended may authenticate again", s.limit, n)
	}
}

// record is end under the lock; it returns the evictions to report, if due.
func (s *sessions) record(tokenKey string, issued time.Time) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.swept) >= sweepEvery {
		for k, m := range s.ended {
			if !now.Before(m.expires()) {
				delete(s.ended, k)
			}
		}
		s.swept = now
	}
	m, known := s.ended[tokenKey]
	switch {
	case issued.Equal(legacyIssued) && !m.legacy:
		m.legacy = true
	case !issued.Equal(legacyIssued) && issued.After(m.issued):
		m.issued = issued
	default:
		return 0, false
	}
	if !known && len(s.ended) >= s.limit {
		s.evictLocked()
	}
	s.ended[tokenKey] = m
	if s.evicted == 0 || now.Sub(s.reported) < evictReportEvery {
		return 0, false
	}
	n := s.evicted
	s.evicted, s.reported = 0, now
	return n, true
}

// evictLocked drops the entry closest to expiring on its own.
func (s *sessions) evictLocked() {
	var first string
	var at time.Time
	for k, m := range s.ended {
		if e := m.expires(); first == "" || e.Before(at) {
			first, at = k, e
		}
	}
	delete(s.ended, first)
	s.evicted++
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
	m, ok := s.ended[tokenKey]
	s.mu.Unlock()
	if !ok {
		return true
	}
	if issued.Equal(legacyIssued) && m.legacy {
		return false
	}
	return issued.After(m.issued)
}
