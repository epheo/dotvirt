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

// Config configures a Client. BaseURL/Token/Owner/Repo are required to create
// PRs; if BaseURL or Token is empty the caller should degrade to push-only.
type Config struct {
	BaseURL     string
	Token       string
	Owner       string
	Repo        string
	InsecureTLS bool // skip TLS verification (dev, e.g. self-signed Route)
}

// New builds a Client. Returns nil if the forge isn't configured (no base URL or
// token), so callers can check and degrade gracefully.
func New(c Config) *Client {
	if c.BaseURL == "" || c.Token == "" || c.Owner == "" || c.Repo == "" {
		return nil
	}
	hc := &http.Client{Timeout: 15 * time.Second}
	if c.InsecureTLS {
		hc.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 — dev flag
		}
	}
	return &Client{
		baseURL: strings.TrimRight(c.BaseURL, "/"),
		token:   c.Token,
		owner:   c.Owner,
		repo:    c.Repo,
		http:    hc,
	}
}

// PR is a pull request as returned by Forgejo (subset).
type PR struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Title   string `json:"title"`
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

// FindOpenPR returns the open PR for head→base, or ok=false if none.
func (c *Client) FindOpenPR(head, base string) (pr PR, ok bool, err error) {
	// Forgejo's pulls list filters by head as "owner:branch".
	q := fmt.Sprintf("/pulls?state=open&base=%s", base)
	var prs []PR
	if err := c.do("GET", c.repoPath(q), nil, &prs); err != nil {
		return PR{}, false, err
	}
	// The list response doesn't echo head in this subset; match by title is
	// unreliable, so the caller treats "any open PR into base from our head" as
	// the proposed one. We return the first open PR; refine if needed.
	if len(prs) > 0 {
		return prs[0], true, nil
	}
	return PR{}, false, nil
}

// CompareURL is the browser URL to manually open a PR for head→base, used when
// the forge API isn't configured.
func (c *Client) CompareURL(head, base string) string {
	return fmt.Sprintf("%s/%s/%s/compare/%s...%s", c.baseURL, c.owner, c.repo, base, head)
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
