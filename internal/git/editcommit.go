package git

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/epheo/dotvirt/internal/model"
)

// ChangesetItem is one change to apply within a CommitChangeset: a rewrite of
// an existing file from its current content (Transform), a whole file
// (NewContent), or the removal of one (Delete). Exactly one mode is used per
// item.
type ChangesetItem struct {
	Path       string // repo-relative manifest path
	Transform  func(current []byte) ([]byte, error)
	NewContent []byte
	Delete     bool

	// BaseBlob, when set, is the version of Path on base the item was made
	// from: a blob ID, or AbsentBlob for a file that did not exist. A delete
	// pinned this way removes only the version it was staged against.
	BaseBlob string
}

// AbsentBlob pins a path that must not exist on base: git's null object ID.
const AbsentBlob = "0000000000000000000000000000000000000000"

// BlobID is the git blob ID of content, the pin of a file read as content.
func BlobID(content []byte) string {
	return plumbing.ComputeHash(plumbing.BlobObject, content).String()
}

// BaseMovedError reports an item whose path no longer holds its BaseBlob on
// base: a merge since staging moved, created or deleted the file.
type BaseMovedError struct{ Path string }

func (e *BaseMovedError) Error() string { return e.Path + " changed on the base branch" }

// ErrBranchMoved reports a branch that no longer holds the head a rebuild
// was leased to: someone else pushed to it, or it was deleted.
var ErrBranchMoved = errors.New("the branch moved off the head dotvirt pushed")

// ErrNoChanges reports a changeset whose every item already matches base -
// nothing to commit because the intent is already true in git.
var ErrNoChanges = errors.New("every staged change already matches git")

// CommitChangeset applies every item to one branch created off base and commits
// them together - the propose step of the draft workflow. A Transform runs on
// the file as the fresh write clone holds it on base, so the proposal is
// against current trunk, not the mirror the draft was staged from. Pushes
// when enabled.
//
// The branch is rebuilt at base and force-pushed (see PushPlan for the
// guarded forms).
func (w *WriteRepo) CommitChangeset(base, branch, message string, items []ChangesetItem, by Author) (CommitResult, error) {
	return w.ProposeChangeset(base, branch, message, items, by, PushPlan{})
}

// PushPlan guards how ProposeChangeset replaces the remote branch.
type PushPlan struct {
	// Lease, when set, is the head the remote branch must still hold: the
	// commit dotvirt last pushed for an open pull request. A branch moved by
	// anyone else, or deleted, is ErrBranchMoved and nothing is pushed.
	Lease string
	// BeforePush runs with the built commit's hash just before the push,
	// which an error from it stops.
	BeforePush func(hash string) error
}

// ProposeChangeset is CommitChangeset under plan.
func (w *WriteRepo) ProposeChangeset(base, branch, message string, items []ChangesetItem, by Author, plan PushPlan) (CommitResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(items) == 0 {
		return CommitResult{}, errors.New("nothing to propose")
	}

	repo, wt, err := w.openWorktree(base)
	if err != nil {
		return CommitResult{}, err
	}
	if err := checkoutBranch(repo, wt, base); err != nil {
		return CommitResult{}, fmt.Errorf("checkout base %q: %w", base, err)
	}
	if err := checkPins(repo, items); err != nil {
		return CommitResult{}, err
	}
	lease := plumbing.NewHash(plan.Lease)
	if !lease.IsZero() && w.push {
		if err := w.holdLease(repo, branch, lease); err != nil {
			return CommitResult{}, err
		}
	}
	if err := resetBranchTo(repo, wt, branch); err != nil {
		return CommitResult{}, err
	}
	res, err := w.commitItems(repo, wt, branch, message, items, by, lease, plan.BeforePush)
	if err != nil {
		return CommitResult{}, err
	}
	if !res.Committed {
		// Classified, not internal: every staged item already matches base (a
		// stale delete, an edit landing on its own value). The caller clears the
		// draft - the intent is fully satisfied - and tells the user so.
		return CommitResult{}, ErrNoChanges
	}
	return res, nil
}

// commitItems is the tail every write shares: apply items to the checked-out
// worktree, commit what changed on branch, run beforePush if any, push under
// lease (see pushBranch). Committed is false when every item already matched
// the tree.
func (w *WriteRepo) commitItems(repo *git.Repository, wt *git.Worktree, branch, message string, items []ChangesetItem, by Author, lease plumbing.Hash, beforePush func(string) error) (CommitResult, error) {
	for _, it := range items {
		if err := applyItem(wt, it); err != nil {
			return CommitResult{}, err
		}
	}
	status, err := wt.Status()
	if err != nil {
		return CommitResult{}, err
	}
	if status.IsClean() {
		return CommitResult{Branch: branch}, nil
	}

	// Author = whoever the change is attributed to; committer = dotvirt (the SA pushing).
	hash, err := wt.Commit(message, &git.CommitOptions{Author: by.signature(), Committer: dotvirtSig()})
	if err != nil {
		return CommitResult{}, fmt.Errorf("commit: %w", err)
	}
	if beforePush != nil {
		if err := beforePush(hash.String()); err != nil {
			return CommitResult{}, err
		}
	}
	if err := w.pushBranch(repo, branch, lease); err != nil {
		return CommitResult{}, err
	}
	return CommitResult{Branch: branch, Committed: true, Hash: hash.String()}, nil
}

// applyItem writes one item into the worktree and stages it. Staging is
// explicit: Commit{All:true} only stages already-tracked files, so a new file
// would otherwise be left out.
func applyItem(wt *git.Worktree, it ChangesetItem) error {
	var content []byte
	switch {
	case it.Delete:
		// wt.Remove deletes the file and stages the removal; nothing to write.
		if _, err := wt.Remove(it.Path); err != nil {
			if errors.Is(err, index.ErrEntryNotFound) {
				// Already absent on base: the intent - this manifest must not
				// be in git - is satisfied. A delete staged against a stale
				// mirror must not wedge the whole propose (found live: every
				// propose 500'd until the user guessed to unstage the item).
				return nil
			}
			return fmt.Errorf("remove %s: %w", it.Path, err)
		}
		return nil
	case it.Transform != nil:
		current, err := readWorktree(wt, it.Path)
		if err != nil {
			return fmt.Errorf("read %s: %w", it.Path, err)
		}
		if content, err = it.Transform(current); err != nil {
			return fmt.Errorf("rewrite %s: %w", it.Path, err)
		}
	case it.NewContent != nil:
		content = it.NewContent
	default:
		return nil
	}
	if err := writeWorktreeFile(wt, model.File{Path: it.Path, Content: content}); err != nil {
		return err
	}
	if _, err := wt.Add(it.Path); err != nil {
		return fmt.Errorf("stage %s: %w", it.Path, err)
	}
	return nil
}

// checkPins refuses the changeset when an item's file no longer holds its
// BaseBlob on the checked-out base: the fresh clone is the authority, where
// the mirror the draft was staged from may lag.
func checkPins(repo *git.Repository, items []ChangesetItem) error {
	head, err := repo.Head()
	if err != nil {
		return err
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return err
	}
	tree, err := commit.Tree()
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.BaseBlob == "" {
			continue
		}
		pin, err := pinIn(tree, it.Path)
		if err != nil {
			return fmt.Errorf("file %s on base: %w", it.Path, err)
		}
		gone := it.Delete && pin == AbsentBlob // already satisfies its delete
		if pin != it.BaseBlob && !gone {
			return &BaseMovedError{Path: it.Path}
		}
	}
	return nil
}

// pinIn is path's blob ID in tree, AbsentBlob when the tree has no such file.
func pinIn(tree *object.Tree, path string) (string, error) {
	f, err := tree.File(path)
	if errors.Is(err, object.ErrFileNotFound) {
		return AbsentBlob, nil
	}
	if err != nil {
		return "", err
	}
	return f.Hash.String(), nil
}

// holdLease checks that the remote branch still holds lease and records it
// as the remote-tracking ref, which go-git's leased push compares against
// again at push time. A deleted branch fails the lease too.
func (w *WriteRepo) holdLease(repo *git.Repository, branch string, lease plumbing.Hash) error {
	remote, err := repo.Remote("origin")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: w.auth()})
	if err != nil {
		return fmt.Errorf("list remote refs: %w", err)
	}
	if headIn(refs, branch) != lease {
		return ErrBranchMoved
	}
	return repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewRemoteReferenceName("origin", branch), lease))
}

// headIn is branch's head among refs, zero when absent.
func headIn(refs []*plumbing.Reference, branch string) plumbing.Hash {
	name := plumbing.NewBranchReferenceName(branch)
	for _, ref := range refs {
		if ref.Name() == name {
			return ref.Hash()
		}
	}
	return plumbing.ZeroHash
}

// RemoteHead is branch's head on the remote, "" when the remote has no such
// branch.
func (w *WriteRepo) RemoteHead(branch string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{w.url}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: w.auth()})
	if err != nil {
		return "", fmt.Errorf("list remote refs: %w", err)
	}
	if h := headIn(refs, branch); !h.IsZero() {
		return h.String(), nil
	}
	return "", nil
}

// resetBranchTo creates branch at the current HEAD (deleting any existing local
// ref first), so the changeset is built fresh on each propose.
func resetBranchTo(repo *git.Repository, wt *git.Worktree, branch string) error {
	ref := plumbing.NewBranchReferenceName(branch)
	if _, err := repo.Reference(ref, true); err == nil {
		_ = repo.Storer.RemoveReference(ref)
	}
	return wt.Checkout(&git.CheckoutOptions{Branch: ref, Create: true})
}

func readWorktree(wt *git.Worktree, path string) ([]byte, error) {
	f, err := wt.Filesystem.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
