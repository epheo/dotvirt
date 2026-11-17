package forge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// RefusedStatus is the HTTP status of a forge declining the account what it
// asked for (401, 403, 404), else 0: an outage, a network failure or no error,
// which a later attempt may get past. A refusal repeats until the forge, its
// owner or the credential changes.
func RefusedStatus(err error) int {
	var se *StatusError
	if !errors.As(err, &se) {
		return 0
	}
	switch se.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return se.Status
	}
	return 0
}

// Refused reports whether err is the forge declining the account (RefusedStatus).
func Refused(err error) bool { return RefusedStatus(err) != 0 }

// refusalTextMax bounds the forge's own words carried into a log line or a
// status condition.
const refusalTextMax = 160

// RefusalText is the forge's own one-line reason for a refused call (its JSON
// "message", e.g. "token does not have at least one of required scope(s)"),
// bounded and stripped of anything but printable ASCII; "" when it gave none
// or answered with a page instead (an HTML body from a proxy says nothing).
func RefusalText(err error) string {
	var se *StatusError
	if !errors.As(err, &se) || RefusedStatus(err) == 0 {
		return ""
	}
	text := strings.TrimSpace(se.Message)
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(text), &body) == nil {
		text = body.Message
	} else if strings.HasPrefix(text, "<") {
		return ""
	}
	text, _, _ = strings.Cut(strings.TrimSpace(text), "\n")
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return -1
		}
		return r
	}, text)
	if len(text) > refusalTextMax {
		text = text[:refusalTextMax] + "..."
	}
	return strings.TrimSpace(text)
}

// OwnerIsUser reports whether the client's owner is a user account (true), or
// no account the forge shows (false); any other answer is an error.
func (c *Client) OwnerIsUser() (bool, error) {
	return c.exists("/api/v1/users/" + c.owner)
}

// CredentialFingerprint names the factory's current token without revealing
// it, so a caller can tell a rotated credential from the one a refusal was met
// with.
func (f *Factory) CredentialFingerprint() string {
	sum := sha256.Sum256([]byte(f.tokenFn()))
	return hex.EncodeToString(sum[:6])
}
