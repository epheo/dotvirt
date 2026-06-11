package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
)

// cookieName is the session cookie holding the signed bearer token.
const cookieName = "dotvirt_session"

// cookieValue encodes the token as base64(token)."."hex(HMAC-SHA256(token)). The
// HMAC binds the value to dotvirt's secret so a client can't forge a cookie for
// an arbitrary token — though the token itself is the real credential; the MAC
// just lets us reject tampering cheaply before spending a TokenReview.
func cookieValue(token string, secret []byte) string {
	return base64.RawURLEncoding.EncodeToString([]byte(token)) + "." + sign(token, secret)
}

// parseCookieValue verifies the HMAC and returns the raw token.
func parseCookieValue(value string, secret []byte) (string, bool) {
	encToken, mac, ok := strings.Cut(value, ".")
	if !ok {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encToken)
	if err != nil {
		return "", false
	}
	token := string(raw)
	if !hmac.Equal([]byte(mac), []byte(sign(token, secret))) {
		return "", false
	}
	return token, true
}

func sign(token string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// setCookie writes the signed session cookie. httpOnly (no JS access), SameSite
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

// readCookie returns the verified token from the session cookie.
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
