// Package forge talks to a Forgejo (Gitea-compatible) server to open and query
// pull requests. Only the small REST surface dotvirt needs is implemented, over
// plain net/http - no SDK.
package forge

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client is a Forgejo API client scoped to one repository of its Factory's forge.
type Client struct {
	f     *Factory
	owner string
	repo  string
}

// TokenSource yields the CURRENT forge token on each call. Resolving per-call
// (rather than capturing a string once) lets a re-minted/rotated token - written
// to a mounted secret file by the operator - take effect without a process
// restart. StaticToken wraps a fixed value (BYO/dev); FileToken reads a mounted
// secret key on each call.
type TokenSource func() string

// StaticToken is a TokenSource that always returns tok (a fixed credential).
func StaticToken(tok string) TokenSource { return func() string { return tok } }

// FileToken is a TokenSource reading path on each call - the projected-secret
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
	baseURL string
	tokenFn TokenSource
	http    *http.Client
}

// NewFactory builds a Factory over the shared forge endpoint. tokenFn is
// resolved per request, so a rotated token takes effect without a restart;
// caFile is an optional PEM bundle for a forge behind the cluster's ingress
// CA, and insecure skips verification instead (dev). Returns nil when the base
// URL is unset (forge disabled) so callers degrade to push-only; a tokenFn that
// currently yields "" still builds a Factory (the token may appear once the
// mounted secret is written).
func NewFactory(baseURL string, tokenFn TokenSource, insecure bool, caFile string) *Factory {
	if baseURL == "" || tokenFn == nil {
		return nil
	}
	return &Factory{
		baseURL: strings.TrimRight(baseURL, "/"),
		tokenFn: tokenFn,
		http:    httpClient(insecure, caFile),
	}
}

// For returns a Client targeting the repo identified by repoURL (e.g.
// https://forge/owner/repo.git -> owner/repo). Returns nil if the owner/repo can't
// be parsed, so the caller degrades to a compare link.
//
// Only the owner/repo is taken from the URL; every call goes to THIS forge. That is
// right for dotvirt's own repos, which is all the write paths ever touch. A caller that
// would read a negative answer as fact must ask SameForge first.
func (f *Factory) For(repoURL string) *Client {
	if f == nil {
		return nil
	}
	owner, repo, ok := ownerRepo(repoURL)
	if !ok {
		return nil
	}
	return &Client{f: f, owner: owner, repo: repo}
}

// SameForge reports whether repoURL names a repo this forge actually serves. For
// discards the URL's host, so without this a repo hosted elsewhere would be looked up
// by path on this forge and its 404 read as "the repo is gone". A URL with no host
// carries no other claim, so it counts as this forge's.
func (f *Factory) SameForge(repoURL string) bool {
	if f == nil {
		return false
	}
	host := parseURL(repoURL).host
	return host == "" || host == parseURL(f.baseURL).host
}

func httpClient(insecure bool, caFile string) *http.Client {
	hc := &http.Client{Timeout: 15 * time.Second}
	if insecure {
		hc.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 - dev flag
		}
		return hc
	}
	if caFile != "" {
		if pool := RootCAs("forge", caFile); pool != nil {
			hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
		}
	}
	return hc
}

// RootCAs returns a pool holding caFile's certificates, or nil when the file is
// unreadable or holds none (logged under component). One tolerance rule for
// every optional CA bundle dotvirt mounts: a bad or lagging bundle keeps the
// caller on the system trust pool, so a missing CA mount degrades to a legible
// TLS error, never a crash.
func RootCAs(component, caFile string) *x509.CertPool {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		log.Printf("%s: CA %s unreadable (%v); staying on the system trust pool", component, caFile, err)
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		log.Printf("%s: CA %s holds no certificates; staying on the system trust pool", component, caFile)
		return nil
	}
	return pool
}

// EnsureRepo creates the client's repo if it doesn't already exist - under its
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

// RepoExists reports whether the client's repo is present on the forge, so a caller
// can tell a dotvirt.io/repo annotation that still resolves from one the forge has
// lost. An error means unreachable, never absent.
func (c *Client) RepoExists() (bool, error) {
	return c.exists(fmt.Sprintf("/api/v1/repos/%s/%s", c.owner, c.repo))
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
	status, _, err := c.f.call("GET", path, c.f.auth(), nil)
	if err != nil {
		return false, err
	}
	switch {
	case status == http.StatusNotFound:
		return false, nil
	case ok2xx(status):
		return true, nil
	default:
		return false, fmt.Errorf("forge GET %s: %s", path, statusText(status))
	}
}

func (c *Client) repoPath(suffix string) string {
	return fmt.Sprintf("/api/v1/repos/%s/%s%s", c.owner, c.repo, suffix)
}

// do performs one repo API call as the forge token: any non-2xx is an error
// carrying the status and the forge's own message, a 2xx body decodes into out.
func (c *Client) do(method, path string, body, out any) error {
	status, data, err := c.f.call(method, path, c.f.auth(), body)
	if err != nil {
		return err
	}
	if !ok2xx(status) {
		return fmt.Errorf("forge %s %s: %s: %s", method, path, statusText(status), strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// authMode is how one request authenticates: the forge token (the runtime's
// calls), a caller-supplied token (a validation probe), or basic credentials
// (token administration as the forge admin).
type authMode struct {
	token          string
	user, password string
}

func tokenAuth(token string) authMode          { return authMode{token: token} }
func basicAuth(user, password string) authMode { return authMode{user: user, password: password} }

// auth is the factory's own token, read per call so a rotation takes effect.
func (f *Factory) auth() authMode { return tokenAuth(f.tokenFn()) }

// call performs one API request against the forge: body marshalled as JSON when
// set, the response body read whole. Every HTTP status comes back as status so
// each caller keeps its own rule (a 404 is "absent" to exists and an error to
// do); only a transport failure is err.
func (f *Factory) call(method, path string, a authMode, body any) (status int, data []byte, err error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if a.user != "" {
		req.SetBasicAuth(a.user, a.password)
	} else {
		req.Header.Set("Authorization", "token "+a.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("forge %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}

func ok2xx(status int) bool { return status >= 200 && status < 300 }

// statusText renders a status the way net/http's Response.Status does ("409
// Conflict"), which the logs and the callers' error strings rely on.
func statusText(status int) string { return fmt.Sprintf("%d %s", status, http.StatusText(status)) }
