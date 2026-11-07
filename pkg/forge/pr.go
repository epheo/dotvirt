// Pull-request operations: opening, finding, and reopening the PR for a
// proposed branch.

package forge

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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
	// UpdatedAt moves with the PR's issue activity (reviews, comments, pushes)
	// but not with commit statuses, which hang off the commit.
	UpdatedAt time.Time `json:"updated_at"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Head      struct {
		Ref    string `json:"ref"`
		Sha    string `json:"sha"`
		RepoID int64  `json:"repo_id"`
	} `json:"head"`
	Base struct {
		Ref    string `json:"ref"`
		RepoID int64  `json:"repo_id"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

// CreatePR opens a pull request from head into base. If one already exists for
// the same head->base, Forgejo returns 409; callers can fall back to FindPR.
func (c *Client) CreatePR(title, body, head, base string) (PR, error) {
	payload := map[string]string{"title": title, "body": body, "head": head, "base": base}
	var pr PR
	if err := c.do("POST", c.repoPath("/pulls"), payload, &pr); err != nil {
		return PR{}, err
	}
	return pr, nil
}

// prPageSize is the page a PR scan asks for. Forgejo caps it at its own
// MAX_RESPONSE_ITEMS, so a short page does not mean the last one.
const prPageSize = 50

// prPageLimit bounds one scan; a PR past it reads as absent.
const prPageLimit = 20

// FindPR returns head's pull request into base: the open one, else the most
// recently updated closed one (merged or not), else ok=false. Matching is
// client-side: Forgejo compares its head filter to the bare branch name, so
// the "owner:branch" form matches nothing, and older versions ignore the
// filters altogether. A fork's PR reusing the branch name never matches.
func (c *Client) FindPR(head, base string) (pr PR, ok bool, err error) {
	if pr, ok, err := c.scanPRs("open", head, base); err != nil || ok {
		return pr, ok, err
	}
	return c.scanPRs("closed", head, base)
}

// scanPRs pages through state's PRs, most recently updated first, until one
// from head into base turns up or a page comes back empty.
func (c *Client) scanPRs(state, head, base string) (PR, bool, error) {
	for page := 1; page <= prPageLimit; page++ {
		q := fmt.Sprintf("/pulls?state=%s&sort=recentupdate&base=%s&limit=%d&page=%d", state, url.QueryEscape(base), prPageSize, page)
		var prs []PR
		if err := c.do("GET", c.repoPath(q), nil, &prs); err != nil {
			return PR{}, false, err
		}
		if len(prs) == 0 {
			break
		}
		for _, pr := range prs {
			if pr.State == state && pr.Head.Ref == head && pr.Base.Ref == base && pr.Head.RepoID == pr.Base.RepoID {
				return pr, true, nil
			}
		}
	}
	return PR{}, false, nil
}

// PR fetches one pull request by number.
func (c *Client) PR(number int) (PR, error) {
	var pr PR
	if err := c.do("GET", c.repoPath(fmt.Sprintf("/pulls/%d", number)), nil, &pr); err != nil {
		return PR{}, err
	}
	return pr, nil
}

// OpenPRs lists the open PRs targeting base, bounded by limit.
func (c *Client) OpenPRs(base string, limit int) ([]PR, error) {
	q := fmt.Sprintf("/pulls?state=open&limit=%d&base=%s", limit, url.QueryEscape(base))
	var prs []PR
	if err := c.do("GET", c.repoPath(q), nil, &prs); err != nil {
		return nil, err
	}
	out := prs[:0]
	for _, pr := range prs {
		if pr.Base.Ref == base {
			out = append(out, pr)
		}
	}
	return out, nil
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

// PREdit changes a pull request; empty fields stay as they are.
type PREdit struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	// State "open" reopens a closed, unmerged pull request.
	State string `json:"state,omitempty"`
}

// EditPR applies edit to pull request number and returns its updated state.
func (c *Client) EditPR(number int, edit PREdit) (PR, error) {
	var pr PR
	if err := c.do("PATCH", c.repoPath("/pulls/"+strconv.Itoa(number)), edit, &pr); err != nil {
		return PR{}, err
	}
	return pr, nil
}

// PullURL is the browser URL of pull request number on this forge.
func (c *Client) PullURL(number int) string {
	return fmt.Sprintf("%s/%s/%s/pulls/%d", c.f.baseURL, c.owner, c.repo, number)
}

// mergeSubject matches the subjects Forgejo writes when it merges a PR: a merge
// commit ("Merge pull request 'title' (#12) from head into base") and a squash
// ("title (#12)"). Rebase merges keep the branch's own subjects and match nothing.
var mergeSubject = regexp.MustCompile(`^(?:Merge pull request '(.*)' \(#(\d+)\) from .+ into .+|(.*) \(#(\d+)\))$`)

// MergeSubject reads the PR title and number back out of a merge commit's
// subject, so history can name a merged change by what the user proposed
// instead of the forge's boilerplate. ok=false for any other subject.
func MergeSubject(subject string) (title string, number int, ok bool) {
	m := mergeSubject.FindStringSubmatch(strings.TrimSpace(subject))
	if m == nil {
		return "", 0, false
	}
	title, num := m[1], m[2]
	if num == "" {
		title, num = m[3], m[4]
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 || title == "" {
		return "", 0, false
	}
	return title, n, true
}
