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
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is a Forgejo API client scoped to one repository.
type Client struct {
	baseURL string // e.g. http://forgejo:3000
	token   string
	owner   string
	repo    string
	http    *http.Client
}

// Factory builds per-project Clients: in multi-tenant mode the PR target (owner +
// repo) varies per project, derived from that project's git repo URL, but the
// forge endpoint + token are shared. Returns nil if the forge isn't configured.
type Factory struct {
	baseURL  string
	token    string
	insecure bool
	http     *http.Client
}

// NewFactory builds a Factory from the shared forge endpoint + token. Returns nil
// when unconfigured so callers degrade to push-only.
func NewFactory(baseURL, token string, insecure bool) *Factory {
	if baseURL == "" || token == "" {
		return nil
	}
	return &Factory{
		baseURL:  strings.TrimRight(baseURL, "/"),
		token:    token,
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
	return &Client{baseURL: f.baseURL, token: f.token, owner: owner, repo: repo, http: f.http}
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
	req.Header.Set("Authorization", "token "+c.token)
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
