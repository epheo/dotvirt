// Package forge talks to a Forgejo (Gitea-compatible) server to open and query
// pull requests. Only the small REST surface dotvirt needs is implemented, over
// plain net/http — no SDK.
package forge

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client is a Forgejo API client scoped to one repository.
type Client struct {
	baseURL string // e.g. http://forgejo:3000
	tokenFn TokenSource
	owner   string
	repo    string
	http    *http.Client
}

// TokenSource yields the CURRENT forge token on each call. Resolving per-call
// (rather than capturing a string once) lets a re-minted/rotated token — written
// to a mounted secret file by the operator — take effect without a process
// restart. StaticToken wraps a fixed value (BYO/dev); FileToken reads a mounted
// secret key on each call.
type TokenSource func() string

// StaticToken is a TokenSource that always returns tok (a fixed credential).
func StaticToken(tok string) TokenSource { return func() string { return tok } }

// FileToken is a TokenSource reading path on each call — the projected-secret
// volume the operator mounts. kubelet updates that file in place on rotation, so
// each forge call picks up the current token. A read error yields "" (the caller
// then behaves as unconfigured/unauthenticated rather than using a stale value).
func FileToken(path string) TokenSource {
	return func() string {
		b, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
}

// Factory builds per-project Clients: in multi-tenant mode the PR target (owner +
// repo) varies per project, derived from that project's git repo URL, but the
// forge endpoint + token are shared. Returns nil if the forge isn't configured.
type Factory struct {
	baseURL  string
	tokenFn  TokenSource
	insecure bool
	http     *http.Client
}

// NewFactory builds a Factory from the shared forge endpoint + a static token.
// Returns nil when unconfigured so callers degrade to push-only. For a rotating
// token (mounted file), use NewFactoryFn.
func NewFactory(baseURL, token string, insecure bool) *Factory {
	if token == "" {
		return nil
	}
	return NewFactoryFn(baseURL, StaticToken(token), insecure)
}

// NewFactoryFn is NewFactory with a TokenSource resolved per request — so a
// rotated token takes effect without restart. Returns nil when the base URL is
// unset (forge disabled); a tokenFn that currently yields "" still builds a
// Factory (the token may appear once the mounted secret is written).
func NewFactoryFn(baseURL string, tokenFn TokenSource, insecure bool) *Factory {
	if baseURL == "" || tokenFn == nil {
		return nil
	}
	return &Factory{
		baseURL:  strings.TrimRight(baseURL, "/"),
		tokenFn:  tokenFn,
		insecure: insecure,
		http:     httpClient(insecure),
	}
}

// For returns a Client targeting the repo identified by repoURL (e.g.
// https://forge/owner/repo.git → owner/repo). Returns nil if the owner/repo can't
// be parsed, so the caller degrades to a compare link.
func (f *Factory) For(repoURL string) *Client {
	if f == nil {
		return nil
	}
	owner, repo, ok := ownerRepo(repoURL)
	if !ok {
		return nil
	}
	return &Client{baseURL: f.baseURL, tokenFn: f.tokenFn, owner: owner, repo: repo, http: f.http}
}

func httpClient(insecure bool) *http.Client {
	hc := &http.Client{Timeout: 15 * time.Second}
	if insecure {
		hc.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 — dev flag
		}
	}
	return hc
}

// EnsureRepo creates the client's repo if it doesn't already exist — under its
// owner organization, auto-initialised so a `main` branch exists for Argo to sync.
// Idempotent; created=true only when it had to create it. This is the one
// imperative bootstrap step a declarative installer can't do (a forge API call, not
// a kubectl apply); the installer operator uses it for the platform repo. The owner
// is expected to be an organization.
func (c *Client) EnsureRepo() (created bool, err error) {
	exists, err := c.exists(fmt.Sprintf("/api/v1/repos/%s/%s", c.owner, c.repo))
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	payload := map[string]any{
		"name":           c.repo,
		"auto_init":      true,
		"default_branch": "main",
		"private":        false,
	}
	if err := c.do("POST", fmt.Sprintf("/api/v1/orgs/%s/repos", c.owner), payload, nil); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureOrg creates the client's owner organization if it doesn't exist (idempotent).
// Used to bootstrap a managed Forgejo's owner org (repos live under the org so a
// single org webhook can cover them all).
func (c *Client) EnsureOrg() error {
	exists, err := c.exists("/api/v1/orgs/" + c.owner)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return c.do("POST", "/api/v1/orgs", map[string]string{"username": c.owner}, nil)
}

// exists reports whether a GET on path returns 2xx (true) or 404 (false); any other
// status is an error. Separate from do() because do() treats every non-2xx as error.
func (c *Client) exists(path string) (bool, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "token "+c.tokenFn())
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("forge GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, nil
	default:
		return false, fmt.Errorf("forge GET %s: %s", path, resp.Status)
	}
}

func (c *Client) repoPath(suffix string) string {
	return fmt.Sprintf("/api/v1/repos/%s/%s%s", c.owner, c.repo, suffix)
}

func (c *Client) do(method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+c.tokenFn())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("forge %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("forge %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
