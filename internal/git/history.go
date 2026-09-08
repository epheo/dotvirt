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

// History returns up to limit recent commits on branch, newest first - the
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
		out = append(out, commitEntry(c))
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

// commitEntry renders one commit as a history row. A forge merge commit is
// authored by whoever clicked Merge; the change itself was authored on the
// merged branch, so a merge is attributed to its second parent's author - the
// same head-branch attribution the task feed uses.
func commitEntry(c *object.Commit) model.Commit {
	h := c.Hash.String()
	subject := firstLine(c.Message)
	author := c.Author.Name
	if c.NumParents() > 1 {
		if head, err := c.Parent(1); err == nil {
			author = head.Author.Name
		}
	}
	return model.Commit{
		Hash:      h,
		ShortHash: h[:8],
		Message:   subject,
		Title:     subject,
		Author:    author,
		When:      c.Author.When.UTC().Format(time.RFC3339),
		Merge:     c.NumParents() > 1,
	}
}

// FileChange is one manifest file a commit touched, with its content on both
// sides: Before is nil for a file the commit added, After nil for one it removed.
type FileChange struct {
	Path   string
	Before []byte
	After  []byte
}

// CommitDiff is one commit and the manifest files it changed against its first
// parent. For a forge merge the first parent is the base branch as it stood, so
// the diff is exactly what the merged PR introduced. templates/ is included:
// the library's history is history too.
type CommitDiff struct {
	Commit model.Commit
	Files  []FileChange
}

// CommitDiff resolves hash and diffs it against its first parent (a root commit
// diffs against the empty tree).
func (r *Repo) CommitDiff(hash string) (CommitDiff, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.commit(hash)
	if err != nil {
		return CommitDiff{}, err
	}
	files, err := firstParentDiff(c)
	if err != nil {
		return CommitDiff{}, err
	}
	return CommitDiff{Commit: commitEntry(c), Files: files}, nil
}

// RevertItems computes the changeset that undoes commit hash: every file the
// commit changed is restored to its first-parent (pre-commit) content, and files
// it added are deleted. The result feeds CommitChangeset as a forward revert - a
// new commit, never a history rewrite. A merge reverts against its first parent,
// the base branch side, which undoes the whole merged PR. The root commit is
// rejected: undoing it would empty the repo.
func (r *Repo) RevertItems(hash string) ([]ChangesetItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.commit(hash)
	if err != nil {
		return nil, err
	}
	if c.NumParents() == 0 {
		return nil, fmt.Errorf("cannot revert the root commit")
	}
	files, err := firstParentDiff(c)
	if err != nil {
		return nil, err
	}
	items := make([]ChangesetItem, 0, len(files))
	for _, f := range files {
		if f.Before == nil {
			items = append(items, ChangesetItem{Path: f.Path, Delete: true})
			continue
		}
		items = append(items, ChangesetItem{Path: f.Path, NewContent: f.Before})
	}
	return items, nil
}

// commit resolves a full hash to its object. Caller holds r.mu.
func (r *Repo) commit(hash string) (*object.Commit, error) {
	c, err := r.repo.CommitObject(plumbing.NewHash(hash))
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", hash, err)
	}
	return c, nil
}

// firstParentDiff lists the YAML files c changed against its first parent, with
// both sides' content, in path order. Caller holds r.mu.
func firstParentDiff(c *object.Commit) ([]FileChange, error) {
	tree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	var parentTree *object.Tree // nil diffs against the empty tree
	if c.NumParents() > 0 {
		parent, err := c.Parent(0)
		if err != nil {
			return nil, err
		}
		if parentTree, err = parent.Tree(); err != nil {
			return nil, err
		}
	}
	changes, err := object.DiffTree(parentTree, tree)
	if err != nil {
		return nil, err
	}
	var out []FileChange
	for _, ch := range changes {
		from, to, err := ch.Files()
		if err != nil {
			return nil, err
		}
		fc := FileChange{Path: ch.To.Name}
		if to == nil {
			fc.Path = ch.From.Name
		}
		if !isYAML(fc.Path) {
			continue
		}
		if from != nil {
			if fc.Before, err = readFile(from); err != nil {
				return nil, fmt.Errorf("read %s: %w", fc.Path, err)
			}
		}
		if to != nil {
			if fc.After, err = readFile(to); err != nil {
				return nil, fmt.Errorf("read %s: %w", fc.Path, err)
			}
		}
		out = append(out, fc)
	}
	return out, nil
}

// firstLine returns the commit subject (first non-empty line of the message).
func firstLine(msg string) string {
	msg = strings.TrimSpace(msg)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return strings.TrimSpace(msg[:i])
	}
	return msg
}
