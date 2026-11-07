package changeset

import (
	"errors"
	"fmt"
	"strings"

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
	read, write, err := c.write(proj)
	if err != nil {
		return model.ProposeResult{}, err
	}
	d, err := read.CommitDiff(hash)
	if err != nil {
		return model.ProposeResult{}, fmt.Errorf("%w: %v", model.ErrNotFound, err)
	}
	items, err := d.RevertItems()
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

	fc := c.forge.For(proj.Repo)
	branch := c.revertBranch(id.Username, proj.Name, hash)
	// A revert of this commit opened before branches carried the identity hash
	// is updated in place rather than duplicated.
	if fc != nil {
		legacy := c.legacyRevertPrefix(id.Username, proj.Name) + shortCommit(hash)
		if pr, ok, err := fc.FindPR(legacy, c.baseBranch); err == nil && ok && pr.State == "open" {
			branch = legacy
		}
	}
	by := git.Author{Name: id.Username, Email: authorEmail(id.Username)}
	res, err := write.CommitChangeset(c.baseBranch, branch, title, items, by)
	if errors.Is(err, git.ErrNoChanges) {
		return model.ProposeResult{}, fmt.Errorf("%w: %s already matches the state before this change", model.ErrInvalid, c.baseBranch)
	}
	if err != nil {
		return model.ProposeResult{}, err
	}
	out := model.ProposeResult{Branch: res.Branch}

	if fc != nil {
		c.openOrRecoverPR(fc, &out, branch, title, body)
	}
	return out, nil
}

// revertBranch is the per-(user, project, commit) branch a revert lands on.
func (r *Reader) revertBranch(user, project, hash string) string {
	return r.revertPrefix(user, project) + shortCommit(hash)
}

// revertPrefix is what every revert branch of (user, project) starts with - how
// the open-PR lane recognizes them. The user segment sits where the task feed's
// attribution expects it (tasks.MergeAuthor); the identity hash keeps two users
// whose names sanitize alike off each other's branches, as on proposedBranch.
func (r *Reader) revertPrefix(user, project string) string {
	return r.legacyRevertPrefix(user, project) + shortHash(user, project) + "-"
}

// legacyRevertPrefix is revertPrefix without the identity hash: how revert
// branches were named before it, with the bare short commit as their tail.
// Still read so those open PRs keep their owner.
func (r *Reader) legacyRevertPrefix(user, project string) string {
	return r.proposed + "/" + tasks.RevertSegment + "/" + refSegment(user) + "/" + refSegment(project) + "-"
}

// ownsLegacyRevert reports whether branch is a hash-less revert branch of
// (user, project). The tail must be a bare commit: a hashed branch whose
// readable prefix collides is someone else's.
func (r *Reader) ownsLegacyRevert(user, project, branch string) bool {
	tail, ok := strings.CutPrefix(branch, r.legacyRevertPrefix(user, project))
	return ok && tail != "" && !strings.Contains(tail, "-")
}

// shortCommit abbreviates a commit hash to 8 chars for branch names + titles.
func shortCommit(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
