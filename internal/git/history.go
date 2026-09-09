package git

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/epheo/dotvirt/internal/model"
)

// History returns up to limit recent commits on branch, newest first, following
// first parents only. On the base branch every PR is one merge; the merged
// branch's own commits are that PR's internals, and a full walk would list them
// again below the root, out of date order.
func (r *Repo) History(branch string, limit int) ([]model.Commit, error) {
	return r.walkHistory(branch, limit, func(*object.Commit) (bool, error) { return true, nil })
}

// FileHistory is History narrowed to the commits that changed path: on the base
// branch, the merged PRs that touched one manifest. A rename reads as a new
// file, and a multi-document file's history includes its siblings' changes.
func (r *Repo) FileHistory(branch, path string, limit int) ([]model.Commit, error) {
	return r.walkHistory(branch, limit, func(c *object.Commit) (bool, error) { return touches(c, path) })
}

// DirHistory is History narrowed to the commits that changed anything under
// dir - on the base branch, the merged PRs that touched one namespace. A
// subtree's entry hash moves with any file below it, so this is one tree
// lookup per commit, like FileHistory.
func (r *Repo) DirHistory(branch, dir string, limit int) ([]model.Commit, error) {
	dir = strings.TrimSuffix(dir, "/")
	return r.walkHistory(branch, limit, func(c *object.Commit) (bool, error) { return touches(c, dir) })
}

// FileAt reads path as it was in commit hash. ErrNoFile when the commit's
// tree has no such file.
func (r *Repo) FileAt(hash, path string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.commit(hash)
	if err != nil {
		return nil, err
	}
	f, err := c.File(path)
	if errors.Is(err, object.ErrFileNotFound) {
		return nil, fmt.Errorf("%w: %s at %s", ErrNoFile, path, hash[:8])
	}
	if err != nil {
		return nil, err
	}
	s, err := f.Contents()
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

// historyScan bounds one walk: a file untouched for this many commits reads as
// having no older history rather than holding r.mu for the whole branch.
const historyScan = 1000

// walkHistory follows first parents from branch's head, keeping the commits
// keep accepts, until limit are kept, the root is reached, or historyScan
// commits have been inspected.
func (r *Repo) walkHistory(branch string, limit int, keep func(*object.Commit) (bool, error)) ([]model.Commit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ref, err := r.repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return nil, fmt.Errorf("resolve branch %q: %w", branch, err)
	}
	c, err := r.repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, err
	}
	out := []model.Commit{}
	for seen := 0; len(out) < limit && seen < historyScan; seen++ {
		ok, err := keep(c)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, commitEntry(c))
		}
		if c.NumParents() == 0 {
			break
		}
		if c, err = c.Parent(0); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// touches reports whether c changed path against its first parent: the blob
// differs, or the file exists on one side only.
func touches(c *object.Commit, path string) (bool, error) {
	now, err := entryHash(c, path)
	if err != nil {
		return false, err
	}
	if c.NumParents() == 0 {
		return now != plumbing.ZeroHash, nil
	}
	parent, err := c.Parent(0)
	if err != nil {
		return false, err
	}
	before, err := entryHash(parent, path)
	if err != nil {
		return false, err
	}
	return now != before, nil
}

// entryHash is path's blob hash in c's tree, ZeroHash when the tree has no
// such file.
func entryHash(c *object.Commit, path string) (plumbing.Hash, error) {
	tree, err := c.Tree()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	e, err := tree.FindEntry(path)
	if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
		return plumbing.ZeroHash, nil
	}
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return e.Hash, nil
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

// ErrNoBranch: the mirror holds no such branch. A head pushed moments ago
// arrives with the next fetch, so callers say "not yet" rather than fail.
var ErrNoBranch = errors.New("branch not in the mirror")

// ErrNoFile: a commit's tree has no such file (FileAt).
var ErrNoFile = errors.New("file not in the commit")

// BranchDiff lists the YAML files head changed since it forked from base: the
// diff from their merge base to head, which is what merging head introduces.
// Without a common ancestor the diff is against base itself.
func (r *Repo) BranchDiff(base, head string) ([]FileChange, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	from, err := r.branchCommit(base)
	if err != nil {
		return nil, err
	}
	to, err := r.branchCommit(head)
	if err != nil {
		return nil, err
	}
	if bases, err := from.MergeBase(to); err != nil {
		return nil, err
	} else if len(bases) > 0 {
		from = bases[0]
	}
	fromTree, err := from.Tree()
	if err != nil {
		return nil, err
	}
	toTree, err := to.Tree()
	if err != nil {
		return nil, err
	}
	return treeDiff(fromTree, toTree)
}

// branchCommit resolves a branch head in the mirror. Caller holds r.mu.
func (r *Repo) branchCommit(branch string) (*object.Commit, error) {
	ref, err := r.repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrNoBranch, branch)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve branch %q: %w", branch, err)
	}
	return r.repo.CommitObject(ref.Hash())
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
	return treeDiff(parentTree, tree)
}

// treeDiff is the YAML files that differ between two trees, with both sides'
// content, in path order. A nil from is the empty tree.
func treeDiff(from, to *object.Tree) ([]FileChange, error) {
	changes, err := object.DiffTree(from, to)
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
