// Pull-request operations: opening, finding, and reopening the PR for a
// proposed branch.

package forge

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// PR is a pull request as returned by Forgejo (subset).
type PR struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	// MergedAt is zero while unmerged (Forgejo sends null; time honors that).
	MergedAt time.Time `json:"merged_at"`
	Title    string    `json:"title"`
	Head     struct {
		Ref string `json:"ref"`
		Sha string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

// CreatePR opens a pull request from head into base. If one already exists for
// the same head->base, Forgejo returns 409; callers can fall back to ListOpenPRs.
func (c *Client) CreatePR(title, body, head, base string) (PR, error) {
	payload := map[string]string{"title": title, "body": body, "head": head, "base": base}
	var pr PR
	if err := c.do("POST", c.repoPath("/pulls"), payload, &pr); err != nil {
		return PR{}, err
	}
	return pr, nil
}

// FindPR returns the PR for head->base regardless of state, or ok=false if none.
// It filters by head branch (Forgejo's "owner:branch" form) and confirms the
// returned PR's head ref matches, so a user's re-propose never matches an
// unrelated PR (e.g. another user's branch or a human feature PR) into the same
// base. An open match is preferred; otherwise the first head-matching PR is
// returned - the reopen target when the prior PR was closed.
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

// MergedPRs lists PRs merged into base, most recently updated first, bounded by
// limit. Forgejo's list endpoint has no merged-state filter, so this lists closed
// PRs and keeps the merged ones targeting base.
func (c *Client) MergedPRs(base string, limit int) ([]PR, error) {
	q := fmt.Sprintf("/pulls?state=closed&sort=recentupdate&limit=%d&base=%s", limit, url.QueryEscape(base))
	var prs []PR
	if err := c.do("GET", c.repoPath(q), nil, &prs); err != nil {
		return nil, err
	}
	out := make([]PR, 0, len(prs))
	for _, pr := range prs {
		if pr.Merged && pr.Base.Ref == base {
			out = append(out, pr)
		}
	}
	return out, nil
}

// Approvals counts the PR's standing approvals: each reviewer's LATEST review
// decides (a later REQUEST_CHANGES withdraws an earlier approval), dismissed
// reviews don't count. Forgejo lists reviews oldest-first.
func (c *Client) Approvals(number int) (int, error) {
	var reviews []struct {
		State     string `json:"state"`
		Dismissed bool   `json:"dismissed"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := c.do("GET", c.repoPath("/pulls/"+strconv.Itoa(number)+"/reviews"), nil, &reviews); err != nil {
		return 0, err
	}
	latest := map[string]string{}
	for _, r := range reviews {
		if r.User.Login == "" || r.Dismissed {
			continue
		}
		// COMMENT reviews carry no verdict; they must not withdraw an approval.
		if r.State == "APPROVED" || r.State == "REQUEST_CHANGES" {
			latest[r.User.Login] = r.State
		}
	}
	n := 0
	for _, s := range latest {
		if s == "APPROVED" {
			n++
		}
	}
	return n, nil
}

// RequiredApprovals reads the branch-protection rule guarding branch, ok=false
// when no rule names it. Exact-name match only: glob rules exist but the base
// branch dotvirt targets is a fixed name. Reading protections can need rights
// the forge token lacks; callers treat any error as unknown, never as "none".
func (c *Client) RequiredApprovals(branch string) (int, bool, error) {
	var rules []struct {
		BranchName        string `json:"branch_name"`
		RuleName          string `json:"rule_name"`
		RequiredApprovals int    `json:"required_approvals"`
	}
	if err := c.do("GET", c.repoPath("/branch_protections"), nil, &rules); err != nil {
		return 0, false, err
	}
	for _, r := range rules {
		if r.BranchName == branch || r.RuleName == branch {
			return r.RequiredApprovals, true, nil
		}
	}
	return 0, false, nil
}

// CombinedStatus returns the combined CI state ("success", "pending",
// "failure", "error") for a commit, or "" when nothing reported one.
func (c *Client) CombinedStatus(sha string) (string, error) {
	var st struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
	}
	if err := c.do("GET", c.repoPath("/commits/"+url.PathEscape(sha)+"/status"), nil, &st); err != nil {
		return "", err
	}
	if st.TotalCount == 0 {
		return "", nil
	}
	return st.State, nil
}

// ReopenPR reopens a closed (unmerged) pull request and returns its updated state.
func (c *Client) ReopenPR(number int) (PR, error) {
	var pr PR
	if err := c.do("PATCH", c.repoPath("/pulls/"+strconv.Itoa(number)), map[string]string{"state": "open"}, &pr); err != nil {
		return PR{}, err
	}
	return pr, nil
}
