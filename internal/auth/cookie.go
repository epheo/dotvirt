package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
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

// cookieKey derives the AES-256 key from SessionSecret so the secret itself is
// never used as a cipher key and a later key purpose cannot collide with it.
func cookieKey(secret []byte) []byte {
	key, err := hkdf.Key(sha256.New, secret, nil, "dotvirt session cookie v2", 32)
	if err != nil {
		panic(err) // only on an impossible length request
	}
	return key
}

func cookieAEAD(secret []byte) cipher.AEAD {
	block, err := aes.NewCipher(cookieKey(secret))
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead
}

// cookieValue seals the token with AES-GCM so the bearer token never sits in
// the browser in clear; the GCM tag also rejects tampering before a TokenReview.
func cookieValue(token string, secret []byte) string {
	aead := cookieAEAD(secret)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(token), []byte(cookiePrefix))
	return cookiePrefix + base64.RawURLEncoding.EncodeToString(sealed)
}

// parseCookieValue opens a sealed value and returns the raw token.
func parseCookieValue(value string, secret []byte) (string, bool) {
	enc, ok := strings.CutPrefix(value, cookiePrefix)
	if !ok {
		return "", false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", false
	}
	aead := cookieAEAD(secret)
	if len(sealed) < aead.NonceSize() {
		return "", false
	}
	nonce, ct := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	raw, err := aead.Open(nil, nonce, ct, []byte(cookiePrefix))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// setCookie writes the sealed session cookie. httpOnly (no JS access), SameSite
// Lax (sent on top-level navigations + same-site requests), Secure when the
// request arrived over TLS.
func setCookie(w http.ResponseWriter, r *http.Request, token string, secret []byte) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    cookieValue(token, secret),
		Path:     "/",
		HttpOnly: true,
		Secure:   isTLS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// readCookie returns the opened token from the session cookie.
func readCookie(r *http.Request, secret []byte) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	return parseCookieValue(c.Value, secret)
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
