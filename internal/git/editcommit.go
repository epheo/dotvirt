package git

import (
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"

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
}

// ErrNoChanges reports a changeset whose every item already matches base -
// nothing to commit because the intent is already true in git.
var ErrNoChanges = errors.New("every staged change already matches git")

// CommitChangeset applies every item to one branch created off base and commits
// them together - the propose step of the draft workflow. A Transform runs on
// the file as the fresh write clone holds it on base, so the proposal is
// against current trunk, not the mirror the draft was staged from. Pushes
// when enabled.
//
// The branch is force-updated, so re-proposing replaces its contents rather than
// stacking commits - keeping one PR per draft.
func (w *WriteRepo) CommitChangeset(base, branch, message string, items []ChangesetItem, by Author) (CommitResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(items) == 0 {
		return CommitResult{}, errors.New("nothing to propose")
	}

	repo, wt, err := w.openWorktree()
	if err != nil {
		return CommitResult{}, err
	}
	if err := checkoutBranch(repo, wt, base); err != nil {
		return CommitResult{}, fmt.Errorf("checkout base %q: %w", base, err)
	}

	// Recreate the branch fresh at base so re-proposes don't stack.
	if err := resetBranchTo(repo, wt, branch); err != nil {
		return CommitResult{}, err
	}
	res, err := w.commitItems(repo, wt, branch, message, items, by)
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
// worktree, commit what changed on branch, push. Committed is false when every
// item already matched the tree.
func (w *WriteRepo) commitItems(repo *git.Repository, wt *git.Worktree, branch, message string, items []ChangesetItem, by Author) (CommitResult, error) {
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
	if err := w.pushBranch(repo, branch); err != nil {
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
