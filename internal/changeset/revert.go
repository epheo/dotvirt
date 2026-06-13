package changeset

import (
	"fmt"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// Revert proposes a forward commit that undoes `hash` in proj's repo, opening (or
// recovering) a PR. The revert restores every file the commit changed to its
// pre-commit state — a new commit reviewable as an ordinary PR, never a history
// rewrite. A revert whose restored files were since changed by a later commit
// will show those as part of the PR diff, which the reviewer catches.
func (c *Coordinator) Revert(id auth.Identity, proj project.ProjectInfo, hash string) (model.ProposeResult, error) {
	if err := requireRepo(proj); err != nil {
		return model.ProposeResult{}, err
	}
	read, write, err := c.repos.Get(proj.Repo)
	if err != nil {
		return model.ProposeResult{}, err
	}
	items, err := read.RevertItems(hash)
	if err != nil {
		return model.ProposeResult{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if len(items) == 0 {
		return model.ProposeResult{}, fmt.Errorf("%w: nothing to revert in that commit", model.ErrInvalid)
	}

	short := shortCommit(hash)
	title := "Revert " + short
	branch := c.revertBranch(id.Username, proj.Name, hash)
	by := git.Author{Name: id.Username, Email: authorEmail(id.Username)}
	res, err := write.CommitChangeset(c.baseBranch, branch, title, items, by)
	if err != nil {
		return model.ProposeResult{}, err
	}
	out := model.ProposeResult{Branch: res.Branch, Pushed: res.Pushed}

	fc := c.forge.For(proj.Repo)
	if fc == nil {
		return out, nil
	}
	if pr, err := fc.CreatePR(title, "Reverts commit "+short+".", branch, c.baseBranch); err == nil {
		out.PRURL, out.PRNumber = pr.HTMLURL, pr.Number
		return out, nil
	}
	// A PR for this revert branch may already exist (re-revert): recover it.
	if existing, ok, ferr := fc.FindPR(branch, c.baseBranch); ferr == nil && ok && existing.State == "open" {
		out.PRURL, out.PRNumber, out.Existing = existing.HTMLURL, existing.Number, true
		return out, nil
	}
	out.CompareURL = fc.CompareURL(branch, c.baseBranch)
	return out, nil
}

// revertBranch is the per-(user, project, commit) branch a revert lands on.
func (c *Coordinator) revertBranch(user, project, hash string) string {
	return c.proposed + "/revert/" + refSegment(user) + "/" + refSegment(project) + "-" + shortCommit(hash)
}

// shortCommit abbreviates a commit hash to 8 chars for branch names + titles.
func shortCommit(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
