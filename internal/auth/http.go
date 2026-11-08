package auth

import (
	"crypto/cipher"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/epheo/dotvirt/internal/restfactory"
)

// userResponse is the public shape of an Identity (the token is never returned).
type userResponse struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups"`
}

// Login validates the posted token and, on success, sets the session cookie.
// Body: {"token": "..."}. Responds 200 {username,groups} or 401.
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if tooBig := (*http.MaxBytesError)(nil); errors.As(err, &tooBig) {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(body.Token)
	id, err := a.Validate(r.Context(), token)
	if err != nil {
		switch {
		case errors.Is(err, ErrRejected):
			http.Error(w, "invalid token", http.StatusUnauthorized)
		case errors.Is(err, ErrThrottled):
			throttled(w, "too many sign-in attempts, try again shortly")
		default:
			http.Error(w, "unable to validate token, try again", http.StatusServiceUnavailable)
		}
		return
	}
	setCookie(w, r, token, false, a.sealer)
	writeJSON(w, http.StatusOK, userResponse{Username: id.Username, Groups: id.Groups})
}

// Logout ends the session the request's cookie seals, with every older session
// of its token, clears the cookie whatever it holds, and answers 204. Opening
// the cookie is the whole check: logout runs ahead of the middleware, so a
// TokenReview that cannot run (budget spent, apiserver down) never leaves a
// session standing that the UI believes over. It returns the session this call
// ended - its token, issue time and origin, no username - for the caller to
// close its streams and revoke an SSO token; a replayed, expired or absent
// cookie ends nothing. A bearer header has no session: its holder keeps the
// raw token, which only the cluster can revoke.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	s, ok := readCookie(r, a.sealer)
	key := restfactory.TokenKey(s.token)
	ended := ok && a.SessionLive(key, s.issued)
	if ended {
		a.sessions.end(key, s.issued)
	}
	clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
	return Identity{Token: s.token, Issued: s.issued, SSO: s.sso}, ended
}

// Me returns the current Identity from context (set by Middleware).
func (a *Authenticator) Me(w http.ResponseWriter, r *http.Request) {
	id, ok := FromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, userResponse{Username: id.Username, Groups: id.Groups})
}

// Middleware authenticates every request except the open endpoints (health and
// login), injecting the Identity into the request context. The token is taken
// from the session cookie or an Authorization: Bearer header (so API clients and
// WebSocket handshakes both work - the cookie rides the WS upgrade request).
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isOpenPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		s, ok := tokenFromRequest(r, a.sealer)
		if !ok {
			if _, err := r.Cookie(cookieName); err == nil {
				clearCookie(w, r) // unreadable: another key, an old format
			}
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		key := restfactory.TokenKey(s.token)
		// Ahead of the verdict cache: a signed-out session's token still validates.
		if !a.sessions.live(key, s.issued) {
			clearCookie(w, r)
			http.Error(w, "session ended", http.StatusUnauthorized)
			return
		}
		id, err := a.validate(r.Context(), s.token, key, true)
		if err != nil {
			// Only a definitive rejection signs the user out (401); a transient
			// inability to validate (API server down/throttled) is 503, so a blip
			// doesn't bounce valid sessions to the login screen.
			switch {
			case errors.Is(err, ErrRejected):
				if !s.issued.IsZero() {
					clearCookie(w, r)
				}
				http.Error(w, "invalid or expired session", http.StatusUnauthorized)
			case errors.Is(err, ErrThrottled):
				throttled(w, "session validation throttled, try again shortly")
			default:
				http.Error(w, "unable to validate session, try again", http.StatusServiceUnavailable)
			}
			return
		}
		id.Issued, id.SSO = s.issued, s.sso
		next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), id)))
	})
}

// throttled answers a refused TokenReview: 429 so the client neither signs out
// (401) nor reads the apiserver as down (503), with the retry hint the budget
// refills on.
func throttled(w http.ResponseWriter, msg string) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, msg, http.StatusTooManyRequests)
}

// isOpenPath reports whether a path bypasses authentication. CORS preflight is
// handled before this (the CORS wrapper answers OPTIONS), so only the genuinely
// public endpoints are listed.
func isOpenPath(path string) bool {
	switch path {
	case "/api/healthz", "/api/login":
		return true
	case "/api/logout":
		// Authenticated by opening the session cookie alone (see Logout).
		return true
	case "/api/auth/methods", "/api/auth/openshift", "/api/auth/callback":
		// The SSO surface: all pre-session by nature (methods tells the login
		// screen what to offer; the redirect/callback pair mint the session).
		return true
	case "/api/v1/getparams.execute":
		// The ArgoCD ApplicationSet plugin generator authenticates with its own
		// shared token (checked in the handler), not a user session/TokenReview.
		return true
	case "/api/webhooks/forge":
		// Forgejo deliveries authenticate by HMAC signature (checked in the
		// handler); the endpoint 404s when no webhook secret is configured.
		return true
	}
	return false
}

// tokenFromRequest pulls the bearer token from the session cookie, falling back
// to an Authorization: Bearer header (a session with no issue time).
func tokenFromRequest(r *http.Request, sealer cipher.AEAD) (session, bool) {
	if s, ok := readCookie(r, sealer); ok {
		return s, true
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if token, ok := strings.CutPrefix(h, "Bearer "); ok {
			return session{token: strings.TrimSpace(token)}, token != ""
		}
	}
	return session{}, false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
