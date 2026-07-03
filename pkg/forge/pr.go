// Pull-request operations: opening, finding, and reopening the PR for a
// proposed branch.

package forge

import (
	"fmt"
	"net/url"
	"strconv"
)

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
