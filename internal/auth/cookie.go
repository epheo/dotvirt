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

// cookiePrefix versions the sealed format: the issue time, then the value.
// legacyPrefix is the form before issue times. Any other value, including the
// HMAC-only form before that, fails to open and reads as logged out.
const (
	cookiePrefix = "v3."
	legacyPrefix = "v2."
)

// legacyIssued is the issue time a legacy cookie reads as. It has none, so it
// reads as issued at this process's start: an upgrade signs no one out, and a
// legacy session ends sessionMaxAge after the process started.
var legacyIssued = time.Now()

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

// cookieValue seals value with its issue time, so the bearer token never sits
// in the browser in clear; the GCM tag also rejects tampering before a
// TokenReview.
func cookieValue(value string, issued time.Time, sealer cipher.AEAD) string {
	plain := binary.BigEndian.AppendUint64(nil, uint64(issued.UnixNano()))
	sealed := sealer.Seal(nil, nil, append(plain, value...), []byte(cookiePrefix))
	return cookiePrefix + base64.RawURLEncoding.EncodeToString(sealed)
}

// parseCookieValue opens a sealed value and returns it with its issue time.
func parseCookieValue(value string, sealer cipher.AEAD) (string, time.Time, bool) {
	if raw, ok := open(value, legacyPrefix, sealer); ok {
		return string(raw), legacyIssued, true
	}
	raw, ok := open(value, cookiePrefix, sealer)
	if !ok || len(raw) < 8 {
		return "", time.Time{}, false
	}
	issued := time.Unix(0, int64(binary.BigEndian.Uint64(raw)))
	return string(raw[8:]), issued, true
}

func open(value, prefix string, sealer cipher.AEAD) ([]byte, bool) {
	enc, ok := strings.CutPrefix(value, prefix)
	if !ok {
		return nil, false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, false
	}
	raw, err := sealer.Open(nil, nil, sealed, []byte(prefix))
	if err != nil {
		return nil, false
	}
	return raw, true
}

// setCookie writes the sealed session cookie, issued now. httpOnly (no JS
// access), SameSite Lax (sent on top-level navigations + same-site requests),
// Secure when the request arrived over TLS. No Max-Age: sessionMaxAge is
// enforced server-side, and the cookie stays a browser-session one.
func setCookie(w http.ResponseWriter, r *http.Request, token string, sealer cipher.AEAD) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    cookieValue(token, time.Now(), sealer),
		Path:     "/",
		HttpOnly: true,
		Secure:   isTLS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// readCookie returns the opened token from the session cookie and its issue
// time.
func readCookie(r *http.Request, sealer cipher.AEAD) (string, time.Time, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", time.Time{}, false
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
