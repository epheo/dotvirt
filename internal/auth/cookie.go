package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"strings"
	"time"
)

// cookieName is the session cookie holding the sealed bearer token.
const cookieName = "dotvirt_session"

// cookiePrefix versions the sealed format: sealedHeader, then the token.
// legacyPrefix is the form before the header. Any other value, including the
// HMAC-only form before that, fails to open and reads as logged out.
const (
	cookiePrefix = "v3."
	legacyPrefix = "v2."
)

// Each sealed value is bound to its purpose as AEAD associated data, so a
// value sealed for one cookie fails the tag of another.
const (
	sessionPurpose = cookiePrefix + "session"
	statePurpose   = cookiePrefix + "oauth-state"
)

// sealedHeader is the issue time (8 bytes, big-endian Unix nanoseconds) and a
// flags byte.
const sealedHeader = 9

const flagSSO byte = 1

// session is what the session cookie seals.
type session struct {
	token  string
	issued time.Time
	// sso marks a token dotvirt's own OAuth client minted, which logout deletes
	// cluster-wide. Never a pasted token: it may be the user's CLI session.
	sso bool
}

// A legacy cookie has no issue time. It reads as issued at the Unix epoch, so
// any logout of its token ends it, and it authenticates until legacyUntil
// whatever restarts come between; after that it reads as logged out.
var (
	legacyIssued = time.Unix(0, 0)
	legacyUntil  = time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)
)

// newSealer is the cookie AEAD: AES-256-GCM under a key derived from
// SessionSecret, so the secret itself is never used as a cipher key and a later
// key purpose cannot collide with it. Each seal draws a random nonce and
// prepends it to the ciphertext. The info string names the key, not the
// format: changing it signs every session out.
func newSealer(secret []byte) cipher.AEAD {
	key, err := hkdf.Key(sha256.New, secret, nil, "dotvirt session cookie v2", 32)
	if err != nil {
		panic(err) // only on an impossible length request
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		panic(err)
	}
	return aead
}

// cookieValue seals s, so the bearer token never sits in the browser in clear;
// the GCM tag also rejects tampering before a TokenReview.
func cookieValue(s session, sealer cipher.AEAD) string {
	plain := binary.BigEndian.AppendUint64(make([]byte, 0, sealedHeader+len(s.token)), uint64(s.issued.UnixNano()))
	var flags byte
	if s.sso {
		flags |= flagSSO
	}
	return seal(append(append(plain, flags), s.token...), sessionPurpose, sealer)
}

// parseCookieValue opens a sealed session.
func parseCookieValue(value string, sealer cipher.AEAD) (session, bool) {
	if raw, ok := open(value, legacyPrefix, legacyPrefix, sealer); ok {
		return session{token: string(raw), issued: legacyIssued}, true
	}
	raw, ok := open(value, cookiePrefix, sessionPurpose, sealer)
	if !ok || len(raw) < sealedHeader {
		return session{}, false
	}
	return session{
		token:  string(raw[sealedHeader:]),
		issued: time.Unix(0, int64(binary.BigEndian.Uint64(raw))),
		sso:    raw[8]&flagSSO != 0,
	}, true
}

// stateValue seals the OAuth state for its cookie.
func stateValue(state string, sealer cipher.AEAD) string {
	return seal([]byte(state), statePurpose, sealer)
}

// parseStateValue opens a sealed OAuth state.
func parseStateValue(value string, sealer cipher.AEAD) (string, bool) {
	raw, ok := open(value, cookiePrefix, statePurpose, sealer)
	return string(raw), ok
}

func seal(plain []byte, purpose string, sealer cipher.AEAD) string {
	return cookiePrefix + base64.RawURLEncoding.EncodeToString(sealer.Seal(nil, nil, plain, []byte(purpose)))
}

func open(value, prefix, purpose string, sealer cipher.AEAD) ([]byte, bool) {
	enc, ok := strings.CutPrefix(value, prefix)
	if !ok {
		return nil, false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, false
	}
	raw, err := sealer.Open(nil, nil, sealed, []byte(purpose))
	if err != nil {
		return nil, false
	}
	return raw, true
}

// setCookie writes the sealed session cookie, issued now; sso says dotvirt's
// OAuth client minted token. httpOnly (no JS access), SameSite Lax (sent on
// top-level navigations + same-site requests), Secure when the request arrived
// over TLS. No Max-Age: sessionMaxAge is enforced server-side, and the cookie
// stays a browser-session one.
func setCookie(w http.ResponseWriter, r *http.Request, token string, sso bool, sealer cipher.AEAD) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    cookieValue(session{token: token, issued: time.Now(), sso: sso}, sealer),
		Path:     "/",
		HttpOnly: true,
		Secure:   isTLS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// readCookie opens the session cookie.
func readCookie(r *http.Request, sealer cipher.AEAD) (session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, false
	}
	return parseCookieValue(c.Value, sealer)
}

// clearCookie expires the session cookie.
func clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isTLS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// isTLS reports whether the request reached dotvirt over HTTPS, accounting for a
// TLS-terminating proxy (X-Forwarded-Proto).
func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
