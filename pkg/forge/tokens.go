// Access-token administration: minting, rotating, and validating the scoped
// token dotvirt's runtime uses, authenticated as the forge admin.

package forge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// MintToken creates a scoped access token for username via BASIC AUTH (not a
// bearer token) — the operator authenticates as the admin a managed Forgejo just
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
	body, err := json.Marshal(map[string]any{"name": tokenName, "scopes": scopes})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", f.baseURL+"/api/v1/users/"+username+"/tokens", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("forge mint token: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("forge mint token: %s: %s", resp.Status, strings.TrimSpace(string(data)))
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
// token NAME as the path id). A 404 (no such token) is success — the goal state is
// "no token of this name", so MintToken can recreate it cleanly on re-mint.
func (f *Factory) deleteToken(username, password, tokenName string) error {
	req, err := http.NewRequest("DELETE", f.baseURL+"/api/v1/users/"+username+"/tokens/"+url.PathEscape(tokenName), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("Accept", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		return fmt.Errorf("forge delete token: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	return fmt.Errorf("forge delete token: %s", resp.Status)
}

// ValidateToken reports whether token authenticates against the forge, via a GET of
// the current-user endpoint. Only 401 means invalid (so the caller re-mints). A 2xx —
// or a 403 — means valid: under Forgejo's granular token scopes a 403 is the token
// authenticating but lacking the read:user scope this endpoint needs, which proves the
// credential is good (treating it as invalid re-mints on every reconcile forever).
// Transport/other errors surface as err so a forge blip isn't mistaken for a bad token.
// Used by the operator to stop trusting a stored token blindly: a Forgejo data reset or
// out-of-band rotation invalidates it (401), and only a re-mint recovers.
func (f *Factory) ValidateToken(token string) (valid bool, err error) {
	if f == nil {
		return false, fmt.Errorf("forge not configured")
	}
	req, err := http.NewRequest("GET", f.baseURL+"/api/v1/user", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("forge validate token: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, nil
	case resp.StatusCode == http.StatusForbidden:
		// Authenticated but forbidden (scope) — a valid credential, not a bad token.
		return true, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return false, nil
	default:
		return false, fmt.Errorf("forge validate token: %s", resp.Status)
	}
}
