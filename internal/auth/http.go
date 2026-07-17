package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(body.Token)
	id, err := a.Validate(r.Context(), token)
	if err != nil {
		if errors.Is(err, ErrRejected) {
			http.Error(w, "invalid token", http.StatusUnauthorized)
		} else {
			http.Error(w, "unable to validate token, try again", http.StatusServiceUnavailable)
		}
		return
	}
	setCookie(w, r, token, a.secret)
	writeJSON(w, http.StatusOK, userResponse{Username: id.Username, Groups: id.Groups})
}

// Logout clears the session cookie. Always 204 (idempotent).
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
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
// WebSocket handshakes both work — the cookie rides the WS upgrade request).
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isOpenPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		token, ok := tokenFromRequest(r, a.secret)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		id, err := a.Validate(r.Context(), token)
		if err != nil {
			// Only a definitive rejection signs the user out (401); a transient
			// inability to validate (API server down/throttled) is 503, so a blip
			// doesn't bounce valid sessions to the login screen.
			if errors.Is(err, ErrRejected) {
				http.Error(w, "invalid or expired session", http.StatusUnauthorized)
			} else {
				http.Error(w, "unable to validate session, try again", http.StatusServiceUnavailable)
			}
			return
		}
		next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), id)))
	})
}

// isOpenPath reports whether a path bypasses authentication. CORS preflight is
// handled before this (the CORS wrapper answers OPTIONS), so only the genuinely
// public endpoints are listed.
func isOpenPath(path string) bool {
	switch path {
	case "/api/healthz", "/api/login":
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
// to an Authorization: Bearer header.
func tokenFromRequest(r *http.Request, secret []byte) (string, bool) {
	if token, ok := readCookie(r, secret); ok {
		return token, true
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if token, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(token), token != ""
		}
	}
	return "", false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
