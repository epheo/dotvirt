package git

import (
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"

	"github.com/epheo/dotvirt/internal/manifest"
)

// EditResult reports the outcome of a CommitChangeset: the working branch the
// changeset landed on and whether it was pushed.
type EditResult struct {
	Branch string `json:"branch"`
	Pushed bool   `json:"pushed"`
}

// ChangesetItem is one change to apply within a CommitChangeset: an edit of an
// existing manifest (Edit set, applied via manifest.ApplyEdit), a brand-new file
// (NewContent set), or the removal of an existing file (Delete set). Exactly one
// mode is used per item.
type ChangesetItem struct {
	Path       string // repo-relative manifest path
	Namespace  string // VM identity (for manifest.ApplyEdit targeting)
	Name       string
	Edit       *manifest.VMEdit // edit mode
	NewContent []byte           // create mode (full manifest)
	Delete     bool             // delete mode: remove Path from the worktree
}

// ErrNoChanges reports a changeset whose every item already matches base -
// nothing to commit because the intent is already true in git.
var ErrNoChanges = errors.New("every staged change already matches git")

// CommitChangeset applies every item to one branch created off base and commits
// them together - the propose step of the draft workflow. Edits re-read the
// current source on base (so the proposal is against current trunk) and apply
// via manifest.ApplyEdit (minimal diff); creates write a new file. Pushes when enabled.
//
// The branch is force-updated, so re-proposing replaces its contents rather than
// stacking commits - keeping one PR per draft.
func (w *WriteRepo) CommitChangeset(base, branch, message string, items []ChangesetItem, by Author) (EditResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(items) == 0 {
		return EditResult{}, errors.New("nothing to propose")
	}

	repo, wt, err := w.openWorktree()
	if err != nil {
		return EditResult{}, err
	}
	if err := checkoutBranch(repo, wt, base); err != nil {
		return EditResult{}, fmt.Errorf("checkout base %q: %w", base, err)
	}

	// Recreate the branch fresh at base so re-proposes don't stack.
	if err := resetBranchTo(repo, wt, branch); err != nil {
		return EditResult{}, err
	}

	for _, it := range items {
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
					continue
				}
				return EditResult{}, fmt.Errorf("remove %s: %w", it.Path, err)
			}
			continue
		case it.Edit != nil:
			original, err := readWorktree(wt, it.Path)
			if err != nil {
				return EditResult{}, fmt.Errorf("read %s on %s: %w", it.Path, base, err)
			}
			content, err = manifest.ApplyEdit(original, it.Namespace, it.Name, *it.Edit)
			if err != nil {
				return EditResult{}, fmt.Errorf("apply edit to %s: %w", it.Path, err)
			}
		case it.NewContent != nil:
			content = it.NewContent
		default:
			continue
		}
		if err := writeWorktreeFile(wt, File{Path: it.Path, Content: content}); err != nil {
			return EditResult{}, err
		}
		if _, err := wt.Add(it.Path); err != nil {
			return EditResult{}, fmt.Errorf("stage %s: %w", it.Path, err)
		}
	}

	status, err := wt.Status()
	if err != nil {
		return EditResult{}, err
	}
	if status.IsClean() {
		// Classified, not internal: every staged item already matches base (a
		// stale delete, an edit landing on its own value). The caller clears the
		// draft - the intent is fully satisfied - and tells the user so.
		return EditResult{}, ErrNoChanges
	}

	// Author = the k8s user who proposed; committer = dotvirt (the SA pushing).
	if _, err := wt.Commit(message, &git.CommitOptions{Author: by.signature(), Committer: dotvirtSig()}); err != nil {
		return EditResult{}, fmt.Errorf("commit: %w", err)
	}

	if err := w.pushBranch(repo, branch); err != nil {
		return EditResult{}, err
	}
	return EditResult{Branch: branch, Pushed: w.push}, nil
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
