package git

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"

	"github.com/epheo/dotvirt/internal/model"
)

// History returns up to limit recent commits on branch, newest first — the
// Changes-pane commit/merge log.
func (r *Repo) History(branch string, limit int) ([]model.Commit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ref, err := r.repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return nil, fmt.Errorf("resolve branch %q: %w", branch, err)
	}
	iter, err := r.repo.Log(&git.LogOptions{From: ref.Hash()})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	out := []model.Commit{}
	err = iter.ForEach(func(c *object.Commit) error {
		h := c.Hash.String()
		out = append(out, model.Commit{
			Hash:      h,
			ShortHash: h[:8],
			Message:   firstLine(c.Message),
			Author:    c.Author.Name,
			When:      c.Author.When.UTC().Format(time.RFC3339),
			Merge:     c.NumParents() > 1,
		})
		if len(out) >= limit {
			return storer.ErrStop
		}
		return nil
	})
	if err != nil && err != storer.ErrStop {
		return nil, err
	}
	return out, nil
}

// RevertItems computes the changeset that undoes commit hash: every file the
// commit changed is restored to its first-parent (pre-commit) content, and files
// it added are deleted. The result feeds CommitChangeset as a forward revert — a
// new commit, never a history rewrite. Root and merge commits are rejected (no
// single parent to restore to).
func (r *Repo) RevertItems(hash string) ([]ChangesetItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, err := r.repo.CommitObject(plumbing.NewHash(hash))
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", hash, err)
	}
	if c.NumParents() == 0 {
		return nil, fmt.Errorf("cannot revert the root commit")
	}
	if c.NumParents() > 1 {
		return nil, fmt.Errorf("cannot revert a merge commit")
	}
	parent, err := c.Parent(0)
	if err != nil {
		return nil, err
	}
	patch, err := parent.Patch(c)
	if err != nil {
		return nil, err
	}
	parentTree, err := parent.Tree()
	if err != nil {
		return nil, err
	}

	var items []ChangesetItem
	for _, fp := range patch.FilePatches() {
		from, to := fp.Files()
		path := ""
		if to != nil {
			path = to.Path()
		} else if from != nil {
			path = from.Path()
		}
		if path == "" || !isYAML(path) {
			continue
		}
		f, ferr := parentTree.File(path)
		if ferr != nil {
			// Absent in the parent → the commit added it → revert removes it.
			items = append(items, ChangesetItem{Path: path, Delete: true})
			continue
		}
		content, rerr := readFile(f)
		if rerr != nil {
			return nil, fmt.Errorf("read %s: %w", path, rerr)
		}
		items = append(items, ChangesetItem{Path: path, NewContent: content})
	}
	return items, nil
}

// firstLine returns the commit subject (first non-empty line of the message).
func firstLine(msg string) string {
	msg = strings.TrimSpace(msg)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return strings.TrimSpace(msg[:i])
	}
	return msg
}
