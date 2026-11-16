package forge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
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

// CredentialFingerprint names the factory's current token without revealing
// it, so a caller can tell a rotated credential from the one a refusal was met
// with.
func (f *Factory) CredentialFingerprint() string {
	sum := sha256.Sum256([]byte(f.tokenFn()))
	return hex.EncodeToString(sum[:6])
}
