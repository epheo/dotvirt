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
		// Label is the head branch's name even after the branch is deleted,
		// when Ref turns into the PR's own refs/pull/<n>/head.
		Label  string `json:"label"`
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

// prPageSize is the page a PR scan asks for.
const prPageSize = 50

// prPageLimit bounds a scan of open PRs; a PR past it reads as absent.
const prPageLimit = 20

// closedPageLimit bounds FindPR's scan of closed PRs: with the head filter
// honored, the branch's own closed PRs come first, and without it an old
// closed PR only decides reopen against create.
const closedPageLimit = 2

// FindPR returns head's pull request into base: the open one, else the most
// recently updated closed one (merged or not), else ok=false. The forge's
// own base/head lookup answers in one call but picks any one PR of the pair,
// so only an open answer ends the search; the list scan follows. Its head
// and base filters are hints - Forgejo compares head to the bare branch
// name, and versions ignore one or both - so matching is client-side. A
// fork's PR reusing the branch name never matches.
func (c *Client) FindPR(head, base string) (pr PR, ok bool, err error) {
	if direct, err := c.pairPR(head, base); err == nil && direct.State == "open" && direct.Base.Ref == base && direct.From(head) {
		return direct, true, nil
	}
	match := func(p PR) bool {
		if p.From(head) {
			pr, ok = p, true
		}
		return ok
	}
	if err = c.eachPR("open", base, head, prPageLimit, match); err != nil || ok {
		return pr, ok, err
	}
	err = c.eachPR("closed", base, head, closedPageLimit, match)
	return pr, ok, err
}

// pairPR is the forge's base/head lookup (Gitea 1.18 and later).
func (c *Client) pairPR(head, base string) (PR, error) {
	segs := strings.Split(head, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	var pr PR
	err := c.do("GET", c.repoPath("/pulls/"+url.PathEscape(base)+"/"+strings.Join(segs, "/")), nil, &pr)
	return pr, err
}

// HeadBranch is the base-repo branch p was opened from. A fork's is "": it
// may carry any name, a proposal branch's included, and the base repo's
// branch of that name is not what the PR merges. A PR whose branch was
// deleted reports its own refs/pull/<n>/head as the ref and keeps the branch
// name only in the label.
func (p PR) HeadBranch() string {
	switch {
	case p.Head.RepoID != p.Base.RepoID:
		return ""
	case p.Head.Label != "" && p.Head.Ref == fmt.Sprintf("refs/pull/%d/head", p.Number):
		return p.Head.Label
	}
	return p.Head.Ref
}

// From reports whether p's head is branch in the base repo. Only a merged
// PR matches by a deleted branch: an open one without its branch cannot be
// pushed to.
func (p PR) From(branch string) bool {
	if p.Head.RepoID != p.Base.RepoID {
		return false
	}
	return p.Head.Ref == branch || p.Merged && p.HeadBranch() == branch
}

// eachPR visits the PRs into base in state, most recently updated first,
// page by page until visit returns true, the forge's X-Total-Count is
// reached, a page comes back empty or pages run out. A short page alone ends
// nothing: the forge caps a page at its own MAX_RESPONSE_ITEMS. head, when
// set, narrows the list on forges that honor it.
func (c *Client) eachPR(state, base, head string, pages int, visit func(PR) bool) error {
	seen := 0
	for page := 1; page <= pages; page++ {
		q := url.Values{"state": {state}, "sort": {"recentupdate"}, "base": {base},
			"limit": {strconv.Itoa(prPageSize)}, "page": {strconv.Itoa(page)}}
		if head != "" {
			q.Set("head", head)
		}
		var prs []PR
		header, err := c.doRead("GET", c.repoPath("/pulls?"+q.Encode()), nil, &prs)
		if err != nil {
			return err
		}
		for _, pr := range prs {
			if pr.State == state && pr.Base.Ref == base && visit(pr) {
				return nil
			}
		}
		seen += len(prs)
		total, terr := strconv.Atoi(header.Get("X-Total-Count"))
		if len(prs) == 0 || terr == nil && seen >= total {
			return nil
		}
	}
	return nil
}

// PR fetches one pull request by number.
func (c *Client) PR(number int) (PR, error) {
	var pr PR
	if err := c.do("GET", c.repoPath(fmt.Sprintf("/pulls/%d", number)), nil, &pr); err != nil {
		return PR{}, err
	}
	return pr, nil
}

// OpenPRs lists every open PR into base.
func (c *Client) OpenPRs(base string) ([]PR, error) {
	var out []PR
	err := c.eachPR("open", base, "", prPageLimit, func(pr PR) bool {
		out = append(out, pr)
		return false
	})
	return out, err
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
