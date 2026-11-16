package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// forgeSessions holds each console session's forge tokens for delegated pull
// requests, by the session's Identity.Key. In memory only, like oidcSessions:
// a restart drops every connection, and the next forge action reconnects
// through a grant the forge remembers. An entry ends with its console session,
// so the provider ending that session (sign-out, expiry, an account it
// suspends) also ends dotvirt's use of the forge token.
type forgeSessions struct {
	mu    sync.Mutex
	m     map[string]*forgeSession
	swept time.Time
	limit int // maxForgeSessions; tests shrink it

	// renew trades a refresh token for the next tokens (Forge.renew).
	renew func(ctx context.Context, refresh string) (forgeTokens, error)
}

// forgeTokens is what one token response gave a connection.
type forgeTokens struct {
	access  string
	refresh string // empty: the forge gave none, so the connection ends with access
	expiry  time.Time
}

type forgeSession struct {
	mu      sync.Mutex // held across a renewal: the forge accepts a refresh token once
	issued  time.Time  // the console session's issue time
	login   string     // the forge account the tokens act as
	tokens  forgeTokens
	renewAt time.Time
	retryAt time.Time // set by a renewal that could not run
	// asked: a connect started during this console session, so the console
	// starts no further one on its own; the user's next save or sign-in asks.
	asked bool
}

// maxForgeSessions backstops the store like maxOIDCSessions.
const maxForgeSessions = 50_000

// ErrForgeNotConnected is a delegated forge action by a user whose console
// session holds no forge connection: they connect, then retry.
var ErrForgeNotConnected = errors.New("your forge account is not connected to this console session")

func newForgeSessions() *forgeSessions {
	return &forgeSessions{m: map[string]*forgeSession{}, limit: maxForgeSessions}
}

// entry returns the live entry under key, creating it for a session issued at
// issued. A zero issued (a bearer header) has no session to hold one.
func (s *forgeSessions) entry(key string, issued time.Time) *forgeSession {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.swept) >= sweepEvery {
		for k, e := range s.m {
			if !now.Before(SessionExpiry(e.issued)) {
				delete(s.m, k)
			}
		}
		s.swept = now
	}
	if e, ok := s.m[key]; ok && now.Before(SessionExpiry(e.issued)) {
		return e
	}
	if len(s.m) >= s.limit {
		s.evictLocked()
	}
	e := &forgeSession{issued: issued}
	s.m[key] = e
	return e
}

// evictLocked forgets the oldest session's connection.
func (s *forgeSessions) evictLocked() {
	var first string
	var at time.Time
	for k, e := range s.m {
		if first == "" || e.issued.Before(at) {
			first, at = k, e.issued
		}
	}
	delete(s.m, first)
	log.Printf("auth: forge connections at their cap of %d: dropped the oldest", s.limit)
}

// get returns the live entry under key, nil when there is none.
func (s *forgeSessions) get(key string) *forgeSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[key]
	if !ok {
		return nil
	}
	if !time.Now().Before(SessionExpiry(e.issued)) {
		delete(s.m, key)
		return nil
	}
	return e
}

// markAsked records that a connect started in the session under key.
func (s *forgeSessions) markAsked(key string, issued time.Time) {
	e := s.entry(key, issued)
	e.mu.Lock()
	e.asked = true
	e.mu.Unlock()
}

// connect stores a fresh connection for the session under key.
func (s *forgeSessions) connect(key string, issued time.Time, login string, t forgeTokens) {
	e := s.entry(key, issued)
	e.mu.Lock()
	e.login, e.tokens, e.asked = login, t, true
	e.renewAt, e.retryAt = renewAt(time.Now(), t.expiry), time.Time{}
	e.mu.Unlock()
}

// disconnect drops the session's tokens and keeps it asked, so the console
// does not reconnect on its own until the next sign-in.
func (s *forgeSessions) disconnect(key string) {
	if e := s.get(key); e != nil {
		e.mu.Lock()
		e.login, e.tokens = "", forgeTokens{}
		e.mu.Unlock()
	}
}

// forget drops the session's entry: the console session ended.
func (s *forgeSessions) forget(key string) {
	s.mu.Lock()
	delete(s.m, key)
	s.mu.Unlock()
}

// status reports the session's connection. Unexpired tokens count; one whose
// renewal is due still counts, as token() renews it on use.
func (s *forgeSessions) status(key string) (connected bool, login string, asked bool) {
	e := s.get(key)
	if e == nil {
		return false, "", false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	live := e.tokens.access != "" && (e.tokens.refresh != "" || time.Now().Before(e.tokens.expiry))
	if !live {
		return false, "", e.asked
	}
	return true, e.login, e.asked
}

// token returns the session's forge login and access token, renewing it when
// due. Concurrent callers wait on one renewal, which spends the refresh token
// once. A refused grant drops the connection (ErrForgeNotConnected); a renewal
// that cannot run serves the token while it lives and retries after probeTTL.
func (s *forgeSessions) token(ctx context.Context, key string) (string, string, error) {
	e := s.get(key)
	if e == nil {
		return "", "", ErrForgeNotConnected
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.tokens
	if t.access == "" {
		return "", "", ErrForgeNotConnected
	}
	now := time.Now()
	if now.Before(e.renewAt) || now.Before(e.retryAt) || t.refresh == "" {
		if now.Before(t.expiry) {
			return e.login, t.access, nil
		}
		if t.refresh == "" {
			e.login, e.tokens = "", forgeTokens{}
			return "", "", ErrForgeNotConnected
		}
		return "", "", errors.New("the forge token expired and its renewal is failing")
	}
	// Detached: a renewal the forge completes and the caller abandons would
	// leave the session holding a spent refresh token.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeTimeout)
	defer cancel()
	next, err := s.renew(rctx, t.refresh)
	switch {
	case errors.Is(err, errGrantEnded):
		log.Printf("auth: forge connection ended by the forge: %v", err)
		e.login, e.tokens = "", forgeTokens{}
		return "", "", ErrForgeNotConnected
	case err != nil:
		log.Printf("auth: renew a forge connection's token: %v", err)
		e.retryAt = now.Add(probeTTL)
		if now.Before(t.expiry) {
			return e.login, t.access, nil
		}
		return "", "", fmt.Errorf("renew the forge token: %w", err)
	}
	e.tokens, e.renewAt, e.retryAt = next, renewAt(time.Now(), next.expiry), time.Time{}
	return e.login, next.access, nil
}
