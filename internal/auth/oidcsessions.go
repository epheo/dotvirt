package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/restfactory"
)

// oidcSessions holds each OIDC session's tokens, which only the server sees:
// the cookie seals a random handle. Every request of a session reads its
// current ID token here, so one renewal serves them all and the refresh token,
// which an issuer may accept once only, is spent once. In memory, like the
// logout record: a restart sends each browser back through the issuer.
type oidcSessions struct {
	mu    sync.Mutex
	m     map[string]*oidcSession // by session key
	swept time.Time
	limit int // maxOIDCSessions; tests shrink it

	// renew trades a refresh token for the next tokens (OIDC.renew); nil
	// until an OIDC flow is configured, when no session can exist.
	renew func(ctx context.Context, refresh string) (oidcTokens, error)
}

// oidcTokens is what one token response gave a session.
type oidcTokens struct {
	id      string // the bearer presented to the apiserver
	refresh string // empty: the issuer gave none, so the session ends with id
	expiry  time.Time
	// subject is the ID token's iss and sub. A renewal must keep it: what is
	// cached under the session key holds for that subject alone.
	subject string
}

type oidcSession struct {
	mu      sync.Mutex // held across a renewal, so its refresh token is spent once
	issued  time.Time
	tokens  oidcTokens
	renewAt time.Time
	retryAt time.Time // set by a renewal that could not run
}

// oidcKeyPrefix starts every OIDC session key, keeping the namespace apart
// from token keys.
const oidcKeyPrefix = "oidc~"

// maxOIDCSessions backstops the store's size: each entry costs an issuer
// sign-in, but a signed-in user could loop them. Past it, the oldest session
// is signed out.
const maxOIDCSessions = 50_000

// errGrantEnded is a renewal the issuer refused for good: the session is over.
var errGrantEnded = errors.New("the issuer ended the grant")

func newOIDCSessions() *oidcSessions {
	return &oidcSessions{m: map[string]*oidcSession{}, limit: maxOIDCSessions}
}

// oidcSessionKey is the session key of the handle a cookie seals. Hashed
// like a token: the handle is as good as one.
func oidcSessionKey(handle string) string {
	return oidcKeyPrefix + restfactory.TokenKey(handle)
}

// renewAt is when tokens received at now are renewed: once three quarters of
// their life has passed, so a renewal that cannot run has the last quarter to
// succeed in.
func renewAt(now, expiry time.Time) time.Time {
	return now.Add(expiry.Sub(now) * 3 / 4)
}

// start records a session signed in at issued and returns its handle.
func (s *oidcSessions) start(t oidcTokens, issued time.Time) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails
	handle := base64.RawURLEncoding.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	if issued.Sub(s.swept) >= sweepEvery {
		for k, sess := range s.m {
			if !issued.Before(SessionExpiry(sess.issued)) {
				delete(s.m, k)
			}
		}
		s.swept = issued
	}
	if len(s.m) >= s.limit {
		s.evictLocked()
	}
	s.m[oidcSessionKey(handle)] = &oidcSession{issued: issued, tokens: t, renewAt: renewAt(issued, t.expiry)}
	return handle
}

// evictLocked signs the oldest session out.
func (s *oidcSessions) evictLocked() {
	var first string
	var at time.Time
	for k, sess := range s.m {
		if first == "" || sess.issued.Before(at) {
			first, at = k, sess.issued
		}
	}
	delete(s.m, first)
	log.Printf("auth: OIDC sessions at their cap of %d: signed out the oldest", s.limit)
}

// get returns the live session under key, nil when there is none.
func (s *oidcSessions) get(key string) *oidcSession {
	if !strings.HasPrefix(key, oidcKeyPrefix) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[key]
	if !ok {
		return nil
	}
	if !time.Now().Before(SessionExpiry(sess.issued)) {
		delete(s.m, key)
		return nil
	}
	return sess
}

func (s *oidcSessions) live(key string) bool {
	return s.get(key) != nil
}

// end removes the session under key and returns its refresh token, for the
// issuer to revoke. Taken after any renewal in flight, so it is the newest.
func (s *oidcSessions) end(key string) (string, bool) {
	s.mu.Lock()
	sess, ok := s.m[key]
	delete(s.m, key)
	s.mu.Unlock()
	if !ok {
		return "", false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.tokens.refresh, true
}

// current returns the ID token of the session under key, renewing it when due.
// A session's concurrent callers wait on its one renewal. A refused grant ends
// the session (ErrSessionEnded). A renewal that cannot run keeps serving the
// token while it lives, and is retried after probeTTL, not by every request.
func (s *oidcSessions) current(ctx context.Context, key string) (string, error) {
	sess := s.get(key)
	if sess == nil {
		return "", ErrSessionEnded
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	now := time.Now()
	t := sess.tokens
	if now.Before(sess.renewAt) || now.Before(sess.retryAt) || t.refresh == "" {
		if now.Before(t.expiry) {
			return t.id, nil
		}
		if t.refresh == "" {
			s.drop(key, sess)
			return "", ErrSessionEnded
		}
		return "", errors.New("the session's token expired and its renewal is failing")
	}
	// Detached: a renewal the issuer completes and the caller abandons would
	// leave the session holding a spent refresh token.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeTimeout)
	defer cancel()
	next, err := s.renew(rctx, t.refresh)
	switch {
	case errors.Is(err, errGrantEnded):
		log.Printf("auth: OIDC session ended by its issuer: %v", err)
		s.drop(key, sess)
		return "", ErrSessionEnded
	case err != nil:
		log.Printf("auth: renew an OIDC session's token: %v", err)
		sess.retryAt = now.Add(probeTTL)
		if now.Before(t.expiry) {
			return t.id, nil
		}
		return "", fmt.Errorf("renew the session's token: %w", err)
	case next.subject != t.subject:
		log.Printf("auth: OIDC session ended: its renewal named another subject")
		s.drop(key, sess)
		return "", ErrSessionEnded
	}
	sess.tokens, sess.renewAt, sess.retryAt = next, renewAt(time.Now(), next.expiry), time.Time{}
	return next.id, nil
}

// drop removes sess from under key, unless a newer session took the key.
func (s *oidcSessions) drop(key string, sess *oidcSession) {
	s.mu.Lock()
	if s.m[key] == sess {
		delete(s.m, key)
	}
	s.mu.Unlock()
}
