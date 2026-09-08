package changeset

import (
	"errors"
	"fmt"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
)

// Revert proposes a forward commit that undoes `hash` in proj's repo, opening (or
// recovering) a PR. The revert restores every file the commit changed to its
// pre-commit state - a new commit reviewable as an ordinary PR, never a history
// rewrite. A merge reverts the whole PR it merged. The PR body carries the
// revert's real diff against the base branch, later changes to those files
// included, so the reviewer sees what merging it does today.
func (c *Coordinator) Revert(id auth.Identity, proj project.ProjectInfo, hash string) (model.ProposeResult, error) {
	if err := requireRepo(proj); err != nil {
		return model.ProposeResult{}, err
	}
	read, write, err := c.repos.Get(proj.Repo)
	if err != nil {
		return model.ProposeResult{}, err
	}
	d, err := read.CommitDiff(hash)
	if err != nil {
		return model.ProposeResult{}, fmt.Errorf("%w: %v", model.ErrNotFound, err)
	}
	items, err := read.RevertItems(hash)
	if err != nil {
		return model.ProposeResult{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if len(items) == 0 {
		return model.ProposeResult{}, fmt.Errorf("%w: nothing to revert in that commit", model.ErrInvalid)
	}
	c.nameByPR(&d.Commit, proj)

	short := shortCommit(hash)
	ref := short
	if d.Commit.PRNumber > 0 {
		ref = fmt.Sprintf("#%d", d.Commit.PRNumber)
	}
	title := fmt.Sprintf("Revert %q (%s)", d.Commit.Title, ref)
	view := model.DraftView{Items: commitItems(c.revertFiles(read, items))}
	view.Warning, _ = c.revertState(read, d.Files)
	body := prBody(view, fmt.Sprintf("Reverts commit %s, %q.", short, d.Commit.Title), id.Username)

	branch := c.revertBranch(id.Username, proj.Name, hash)
	by := git.Author{Name: id.Username, Email: authorEmail(id.Username)}
	res, err := write.CommitChangeset(c.baseBranch, branch, title, items, by)
	if errors.Is(err, git.ErrNoChanges) {
		return model.ProposeResult{}, fmt.Errorf("%w: %s already matches the state before this change", model.ErrInvalid, c.baseBranch)
	}
	if err != nil {
		return model.ProposeResult{}, err
	}
	out := model.ProposeResult{Branch: res.Branch, Pushed: res.Pushed}

	fc := c.forge.For(proj.Repo)
	if fc == nil {
		return out, nil
	}
	if pr, err := fc.CreatePR(title, body, branch, c.baseBranch); err == nil {
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
	return c.revertPrefix(user, project) + shortCommit(hash)
}

// revertPrefix is what every revert branch of (user, project) starts with - how
// the open-PR lane recognizes them. The user segment sits where the task feed's
// attribution expects it (tasks.MergeAuthor).
func (c *Coordinator) revertPrefix(user, project string) string {
	return c.proposed + "/" + tasks.RevertSegment + "/" + refSegment(user) + "/" + refSegment(project) + "-"
}

// shortCommit abbreviates a commit hash to 8 chars for branch names + titles.
func shortCommit(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
