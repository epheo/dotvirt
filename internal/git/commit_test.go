package git

import (
	"github.com/epheo/dotvirt/internal/model"
	"github.com/go-git/go-git/v5/plumbing"
)

// Commit is the tests' direct write: files onto branch as dotvirt itself, on top of the branch's
// real remote state (created from the default branch when it doesn't exist yet).
// Identical content commits nothing.
func (w *WriteRepo) Commit(branch, message string, files []model.File) (CommitResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	repo, wt, err := w.openWorktree(branch)
	if err != nil {
		return CommitResult{}, err
	}
	if err := checkoutBranch(repo, wt, branch); err != nil {
		return CommitResult{}, err
	}
	items := make([]ChangesetItem, len(files))
	for i, f := range files {
		items[i] = ChangesetItem{Path: f.Path, NewContent: f.Content}
	}
	return w.commitItems(repo, wt, branch, message, items, Author{}, plumbing.ZeroHash, nil)
}
