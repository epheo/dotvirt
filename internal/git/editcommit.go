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

	// BaseBlob, when set, is the blob Path must still hold where the commit
	// lands, base and an extended branch alike: the version NewContent was
	// computed against.
	BaseBlob string
}

// BaseMovedError reports an item whose path no longer holds its BaseBlob on
// Branch: base moved past it, or an extended branch already changed the file.
type BaseMovedError struct{ Path, Branch string }

func (e *BaseMovedError) Error() string { return e.Path + " changed on " + e.Branch }

// ErrBranchMoved reports a push refused because the branch moved past the
// head the commit was built on: someone else pushed meanwhile.
var ErrBranchMoved = errors.New("the branch moved while the commit was built")

// ErrNoChanges reports a changeset whose every item already matches base -
// nothing to commit because the intent is already true in git.
var ErrNoChanges = errors.New("every staged change already matches git")

// CommitChangeset applies every item to one branch created off base and commits
// them together - the propose step of the draft workflow. A Transform runs on
// the file as the fresh write clone holds it on base, so the proposal is
// against current trunk, not the mirror the draft was staged from. Pushes
// when enabled.
//
// The branch is rebuilt at base and force-pushed: it is for a branch no open
// pull request is reviewing (see ExtendChangeset).
func (w *WriteRepo) CommitChangeset(base, branch, message string, items []ChangesetItem, by Author) (CommitResult, error) {
	return w.commitChangeset(base, branch, false, message, items, by)
}

// ExtendChangeset is CommitChangeset on top of branch as the remote holds it -
// an open pull request's head - so what the branch already proposes stays and
// a Transform sees it. The push is leased to that head: a branch moved by
// someone else meanwhile is refused, never overwritten. A branch the remote
// lacks forks from base.
func (w *WriteRepo) ExtendChangeset(base, branch, message string, items []ChangesetItem, by Author) (CommitResult, error) {
	return w.commitChangeset(base, branch, true, message, items, by)
}

func (w *WriteRepo) commitChangeset(base, branch string, extend bool, message string, items []ChangesetItem, by Author) (CommitResult, error) {
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
	if err := checkPins(repo, base, items); err != nil {
		return CommitResult{}, err
	}

	var lease plumbing.Hash
	if extend {
		if lease, err = w.fetchBranch(repo, branch); err != nil {
			return CommitResult{}, err
		}
	}
	if lease.IsZero() {
		err = resetBranchTo(repo, wt, branch)
	} else if err = checkoutBranch(repo, wt, branch); err == nil {
		err = checkPins(repo, branch, items)
	}
	if err != nil {
		return CommitResult{}, err
	}
	res, err := w.commitItems(repo, wt, branch, message, items, by, lease)
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
// worktree, commit what changed on branch, push under lease (see pushBranch).
// Committed is false when every item already matched the tree.
func (w *WriteRepo) commitItems(repo *git.Repository, wt *git.Worktree, branch, message string, items []ChangesetItem, by Author, lease plumbing.Hash) (CommitResult, error) {
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
// BaseBlob on the checked-out branch: the fresh clone is the authority, where
// the mirror the draft was staged from may lag.
func checkPins(repo *git.Repository, branch string, items []ChangesetItem) error {
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
		f, err := tree.File(it.Path)
		if errors.Is(err, object.ErrFileNotFound) || err == nil && f.Hash.String() != it.BaseBlob {
			return &BaseMovedError{Path: it.Path, Branch: branch}
		}
		if err != nil {
			return fmt.Errorf("file %s on %s: %w", it.Path, branch, err)
		}
	}
	return nil
}

// fetchBranch fetches branch's remote head into the shallow clone as its
// remote-tracking ref and returns it; zero when the remote has no such branch.
func (w *WriteRepo) fetchBranch(repo *git.Repository, branch string) (plumbing.Hash, error) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	tracking := plumbing.NewRemoteReferenceName("origin", branch)
	err := repo.FetchContext(ctx, &git.FetchOptions{
		Auth:     w.auth(),
		RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf("+%s:%s", plumbing.NewBranchReferenceName(branch), tracking))},
		Depth:    1,
	})
	if isMissingRef(err) {
		return plumbing.ZeroHash, nil
	}
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return plumbing.ZeroHash, fmt.Errorf("fetch %s: %w", branch, err)
	}
	ref, err := repo.Reference(tracking, true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("fetch %s: %w", branch, err)
	}
	return ref.Hash(), nil
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
