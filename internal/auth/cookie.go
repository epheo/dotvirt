package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
)

// cookieName is the session cookie holding the sealed bearer token.
const cookieName = "dotvirt_session"

// cookiePrefix versions the sealed format; any other value, including the
// older HMAC-only form, fails to open and reads as logged out.
const cookiePrefix = "v2."

// newSealer is the cookie AEAD: AES-256-GCM under a key derived from
// SessionSecret, so the secret itself is never used as a cipher key and a later
// key purpose cannot collide with it. Each seal draws a random nonce and
// prepends it to the ciphertext.
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

// cookieValue seals the token so the bearer token never sits in the browser
// in clear; the GCM tag also rejects tampering before a TokenReview.
func cookieValue(token string, sealer cipher.AEAD) string {
	sealed := sealer.Seal(nil, nil, []byte(token), []byte(cookiePrefix))
	return cookiePrefix + base64.RawURLEncoding.EncodeToString(sealed)
}

// parseCookieValue opens a sealed value and returns the raw token.
func parseCookieValue(value string, sealer cipher.AEAD) (string, bool) {
	enc, ok := strings.CutPrefix(value, cookiePrefix)
	if !ok {
		return "", false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", false
	}
	raw, err := sealer.Open(nil, nil, sealed, []byte(cookiePrefix))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// setCookie writes the sealed session cookie. httpOnly (no JS access), SameSite
// Lax (sent on top-level navigations + same-site requests), Secure when the
// request arrived over TLS.
func setCookie(w http.ResponseWriter, r *http.Request, token string, sealer cipher.AEAD) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    cookieValue(token, sealer),
		Path:     "/",
		HttpOnly: true,
		Secure:   isTLS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// readCookie returns the opened token from the session cookie.
func readCookie(r *http.Request, sealer cipher.AEAD) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
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
