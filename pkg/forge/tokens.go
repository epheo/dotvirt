// Access-token administration: minting, rotating, and validating the scoped
// token dotvirt's runtime uses, authenticated as the forge admin.

package forge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrUnauthorized marks a 401 from the forge admin API: the credential itself was
// rejected, not the transport. Callers branch on it to surface a password/token
// mismatch distinctly from a forge outage.
var ErrUnauthorized = errors.New("401 unauthorized")

// MintToken creates a scoped access token for username via BASIC AUTH (not a
// bearer token) - the operator authenticates as the admin a managed Forgejo just
// created and mints the narrow token dotvirt's runtime then uses. Returns the token.
func (f *Factory) MintToken(username, password, tokenName string, scopes []string) (string, error) {
	if f == nil {
		return "", fmt.Errorf("forge not configured")
	}
	// Re-mint safe: Forgejo 400s on a duplicate token name, so a re-mint (the stored
	// token was rejected) must first delete the prior token of this name. The raw
	// secret of an existing token can't be re-read, so rotation is delete-then-create.
	if err := f.deleteToken(username, password, tokenName); err != nil {
		return "", err
	}
	status, data, err := f.call("POST", "/api/v1/users/"+username+"/tokens", basicAuth(username, password),
		map[string]any{"name": tokenName, "scopes": scopes})
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized {
		return "", fmt.Errorf("forge mint token: %w", ErrUnauthorized)
	}
	if !ok2xx(status) {
		return "", fmt.Errorf("forge mint token: %s: %s", statusText(status), strings.TrimSpace(string(data)))
	}
	var out struct {
		Sha1 string `json:"sha1"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	return out.Sha1, nil
}

// deleteToken removes a named access token via basic auth (Forgejo accepts the
// token NAME as the path id). A 404 (no such token) is success - the goal state is
// "no token of this name", so MintToken can recreate it cleanly on re-mint.
func (f *Factory) deleteToken(username, password, tokenName string) error {
	status, _, err := f.call("DELETE", "/api/v1/users/"+username+"/tokens/"+url.PathEscape(tokenName), basicAuth(username, password), nil)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusNotFound || ok2xx(status):
		return nil
	case status == http.StatusUnauthorized:
		return fmt.Errorf("forge delete token: %w", ErrUnauthorized)
	default:
		return fmt.Errorf("forge delete token: %s", statusText(status))
	}
}

// ValidateToken reports whether token authenticates against the forge, via a GET of
// the current-user endpoint. Only 401 means invalid (so the caller re-mints). A 2xx -
// or a 403 - means valid: under Forgejo's granular token scopes a 403 is the token
// authenticating but lacking the read:user scope this endpoint needs, which proves the
// credential is good (treating it as invalid re-mints on every reconcile forever).
// Transport/other errors surface as err so a forge blip isn't mistaken for a bad token.
// Used by the operator to stop trusting a stored token blindly: a Forgejo data reset or
// out-of-band rotation invalidates it (401), and only a re-mint recovers.
func (f *Factory) ValidateToken(token string) (valid bool, err error) {
	if f == nil {
		return false, fmt.Errorf("forge not configured")
	}
	status, _, err := f.call("GET", "/api/v1/user", tokenAuth(token), nil)
	if err != nil {
		return false, err
	}
	switch {
	case ok2xx(status) || status == http.StatusForbidden:
		return true, nil
	case status == http.StatusUnauthorized:
		return false, nil
	default:
		return false, fmt.Errorf("forge validate token: %s", statusText(status))
	}
}
