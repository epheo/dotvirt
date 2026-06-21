// Package forge talks to a Forgejo (Gitea-compatible) server to open and query
// pull requests. Only the small REST surface dotvirt needs is implemented, over
// plain net/http — no SDK.
package forge

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
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

// PR is a pull request as returned by Forgejo (subset).
type PR struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Title   string `json:"title"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// CreatePR opens a pull request from head into base. If one already exists for
// the same head→base, Forgejo returns 409; callers can fall back to ListOpenPRs.
func (c *Client) CreatePR(title, body, head, base string) (PR, error) {
	payload := map[string]string{"title": title, "body": body, "head": head, "base": base}
	var pr PR
	if err := c.do("POST", c.repoPath("/pulls"), payload, &pr); err != nil {
		return PR{}, err
	}
	return pr, nil
}

// FindPR returns the PR for head→base regardless of state, or ok=false if none.
// It filters by head branch (Forgejo's "owner:branch" form) and confirms the
// returned PR's head ref matches, so a user's re-propose never matches an
// unrelated PR (e.g. another user's branch or a human feature PR) into the same
// base. An open match is preferred; otherwise the first head-matching PR is
// returned — the reopen target when the prior PR was closed.
func (c *Client) FindPR(head, base string) (pr PR, ok bool, err error) {
	q := fmt.Sprintf("/pulls?state=all&base=%s&head=%s:%s", url.QueryEscape(base), url.QueryEscape(c.owner), url.QueryEscape(head))
	var prs []PR
	if err := c.do("GET", c.repoPath(q), nil, &prs); err != nil {
		return PR{}, false, err
	}
	var fallback *PR
	for i := range prs {
		if prs[i].Head.Ref != head {
			continue
		}
		if prs[i].State == "open" {
			return prs[i], true, nil
		}
		if fallback == nil {
			fallback = &prs[i]
		}
	}
	if fallback != nil {
		return *fallback, true, nil
	}
	return PR{}, false, nil
}

// ReopenPR reopens a closed (unmerged) pull request and returns its updated state.
func (c *Client) ReopenPR(number int) (PR, error) {
	var pr PR
	if err := c.do("PATCH", c.repoPath("/pulls/"+strconv.Itoa(number)), map[string]string{"state": "open"}, &pr); err != nil {
		return PR{}, err
	}
	return pr, nil
}

// hook is a repo webhook as returned by Forgejo (subset).
type hook struct {
	ID     int               `json:"id"`
	Config map[string]string `json:"config"`
}

// EnsureWebhook registers a push+pull_request webhook on the client's REPO delivering
// to targetURL (HMAC-signed with secret). Idempotent and safe on every sweep: it
// migrates the hook in place when targetURL's host changes and re-asserts the secret at
// most once per process. See ensureHook.
func (c *Client) EnsureWebhook(targetURL, secret string) error {
	return c.ensureHook(c.repoPath("/hooks"), targetURL, secret)
}

// EnsureOrgWebhook registers the same push+pull_request webhook on the client's
// ORGANIZATION rather than a single repo, so one hook covers every repo in the org —
// present and future. Used to point ArgoCD at all project repos with a single
// registration, with no per-repo enumeration. Same reconcile semantics as
// EnsureWebhook (see ensureHook).
func (c *Client) EnsureOrgWebhook(targetURL, secret string) error {
	return c.ensureHook(fmt.Sprintf("/api/v1/orgs/%s/hooks", c.owner), targetURL, secret)
}

// ensureHook converges a single "gitea" (Forgejo-compatible) push+pull_request webhook
// delivering to targetURL within the given hooks collection (repo- or org-level).
//
// The hook is identified by its URL PATH, not its full URL. The host legitimately
// changes — an external Route giving way to the in-cluster Service — and matching on the
// path migrates that one hook in place rather than orphaning the old hook and POSTing a
// duplicate that double-delivers. Extra hooks sharing the path (from an earlier
// migration or a manual add) are deleted, so deliveries never split across a
// half-configured second hook.
//
// Forgejo never echoes a hook's stored secret, so a converged hook is indistinguishable
// from one carrying a stale/rotated secret that 403s every delivery. The fingerprint
// cache (hookSecrets) records the secret last written per hook, so the secret is
// re-asserted at most once per process — on first sight or after a rotation — not on
// every sweep. That keeps steady-state sweeps write-free against Forgejo's single-replica
// sqlite, and leaves a converged hook exactly as the forge has it (active or not) instead
// of fighting its failure-driven auto-disable.
func (c *Client) ensureHook(hooksPath, targetURL, secret string) error {
	var hooks []hook
	if err := c.do("GET", hooksPath, nil, &hooks); err != nil {
		return err
	}
	cfg := map[string]string{"url": targetURL, "content_type": "json", "secret": secret}
	// One desired payload; the create API additionally requires "type" (the edit API
	// rejects it), added on the POST branch only.
	desired := map[string]any{"active": true, "events": []string{"push", "pull_request"}, "config": cfg}

	targetPath := urlPath(targetURL)
	var ours []hook
	for _, h := range hooks {
		if urlPath(h.Config["url"]) == targetPath {
			ours = append(ours, h)
		}
	}
	if len(ours) == 0 {
		return c.do("POST", hooksPath, withCreateType(desired), nil)
	}

	// Reconcile the first match in place; PATCH only on a real change — a host migration
	// or a secret the cache hasn't seen this process — so a re-enable (active:true) and a
	// secret rewrite happen exactly when they recover something, not every sweep. Record
	// only after the write lands, or a failed PATCH would falsely mark the hook converged.
	primary := ours[0]
	key := fmt.Sprintf("%s#%d", hooksPath, primary.ID)
	if primary.Config["url"] != targetURL || !hookSecretMatches(key, secret) {
		if err := c.do("PATCH", fmt.Sprintf("%s/%d", hooksPath, primary.ID), desired, nil); err != nil {
			return err
		}
		recordHookSecret(key, secret)
	}
	for _, dup := range ours[1:] {
		if err := c.do("DELETE", fmt.Sprintf("%s/%d", hooksPath, dup.ID), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// hookSecrets fingerprints the secret last written to each reconciled hook, keyed by
// "{collection}#{id}". It exists because Forgejo never echoes a hook's stored secret:
// without it ensureHook could not tell a converged hook from one needing its secret
// re-asserted, and would PATCH on every sweep. Process-lifetime state — a restart
// re-asserts once, which is the intended recovery.
var (
	hookSecretsMu sync.Mutex
	hookSecrets   = map[string]string{}
)

func hookFingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// hookSecretMatches reports whether key was last reconciled with this secret.
func hookSecretMatches(key, secret string) bool {
	hookSecretsMu.Lock()
	defer hookSecretsMu.Unlock()
	return hookSecrets[key] == hookFingerprint(secret)
}

// recordHookSecret remembers the secret just written to key.
func recordHookSecret(key, secret string) {
	hookSecretsMu.Lock()
	defer hookSecretsMu.Unlock()
	hookSecrets[key] = hookFingerprint(secret)
}

// withCreateType copies a hook payload and sets the create-only "type" field.
func withCreateType(base map[string]any) map[string]any {
	out := make(map[string]any, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["type"] = "gitea"
	return out
}

// urlPath returns the path component of a URL, or raw unchanged if it doesn't parse —
// enough to identify a webhook across a host change without binding to scheme/host/port.
func urlPath(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		return u.Path
	}
	return raw
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

// CompareURL is the browser URL to manually open a PR for head→base, used when
// the forge API isn't configured.
func (c *Client) CompareURL(head, base string) string {
	return fmt.Sprintf("%s/%s/%s/compare/%s...%s", c.baseURL, c.owner, c.repo, base, head)
}

func (c *Client) repoPath(suffix string) string {
	return fmt.Sprintf("/api/v1/repos/%s/%s%s", c.owner, c.repo, suffix)
}

// ownerRepo extracts the owner and repo from a Forgejo/Gitea repo URL. It takes
// the last two path segments and strips a trailing ".git", so
// https://forge.example/dotvirt/team-a.git → ("dotvirt", "team-a"). It fails
// closed (ok=false) on anything it can't parse cleanly, so the caller degrades to
// a compare link rather than building a malformed API path.
func ownerRepo(repoURL string) (owner, repo string, ok bool) {
	s := repoURL
	// Drop any query string / fragment before touching the path or the .git suffix.
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")
	// Drop scheme + host: keep the path.
	if i := strings.Index(s, "://"); i >= 0 {
		if slash := strings.IndexByte(s[i+3:], '/'); slash >= 0 {
			s = s[i+3+slash+1:]
		} else {
			return "", "", false
		}
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 {
		return "", "", false
	}
	owner, repo = parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || repo == "" {
		return "", "", false
	}
	return owner, repo, true
}

// NormalizeRepoURL canonicalizes a git repo URL for equality comparison: trimmed,
// no trailing slash, a single trailing ".git" stripped, lowercased. It lets the
// same repo written three ways — the forge clone_url (…​.git), the html_url (no
// .git), and a trailing-slash annotation — resolve to one key, so a push webhook
// reliably finds the repo's poller (RepoSet) and its managing ArgoCD Application
// (argo.Snapshot.RefreshForRepo). Returns "" for an empty/blank input.
func NormalizeRepoURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	// Lowercase first so a mixed-case ".GIT" suffix or host still canonicalizes.
	u = strings.ToLower(u)
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, ".git")
	return u
}

// OwnerPrefixURL is the forge owner URL ("scheme://host/.../<owner>") of a repo
// URL — the prefix Argo longest-prefix-matches to attach one repo-credential to
// every repo under that owner. Unlike path.Dir, it preserves the "://" in the
// scheme (path.Dir collapses it to ":/", yielding a prefix Argo never matches).
// Returns the input unchanged when there's no repo segment to strip.
func OwnerPrefixURL(repoURL string) string {
	s := strings.TrimSuffix(strings.TrimRight(repoURL, "/"), ".git")
	// Find where the path starts, after scheme://host, so we never cut into "://".
	pathStart := 0
	if i := strings.Index(s, "://"); i >= 0 {
		pathStart = i + 3
	}
	slash := strings.LastIndexByte(s[pathStart:], '/')
	if slash <= 0 {
		return repoURL // no owner/repo path segments to strip
	}
	return s[:pathStart+slash]
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
