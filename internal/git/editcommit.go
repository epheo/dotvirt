package git

import (
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/epheo/dotvirt/internal/manifest"
)

// EditResult reports the outcome of a CommitChangeset: the working branch the
// changeset landed on and whether it was pushed.
type EditResult struct {
	Branch string `json:"branch"`
	Pushed bool   `json:"pushed"`
}

// ChangesetItem is one change to apply within a CommitChangeset: either an edit
// of an existing manifest (Edit set, applied via manifest.ApplyEdit) or a brand-new file
// (NewContent set). Exactly one mode is used per item.
type ChangesetItem struct {
	Path       string // repo-relative manifest path
	Namespace  string // VM identity (for manifest.ApplyEdit targeting)
	Name       string
	Edit       *manifest.VMEdit // edit mode
	NewContent []byte           // create mode (full manifest)
}

// CommitChangeset applies every item to one branch created off base and commits
// them together — the propose step of the draft workflow. Edits re-read the
// current source on base (so the proposal is against current trunk) and apply
// via manifest.ApplyEdit (minimal diff); creates write a new file. Pushes when enabled.
//
// The branch is force-updated, so re-proposing replaces its contents rather than
// stacking commits — keeping one PR per draft.
func (w *WriteRepo) CommitChangeset(base, branch, message string, items []ChangesetItem, by Author) (EditResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(items) == 0 {
		return EditResult{}, errors.New("nothing to propose")
	}

	repo, err := git.Clone(memory.NewStorage(), memfs.New(), &git.CloneOptions{URL: w.url, Auth: w.auth})
	if err != nil {
		return EditResult{}, fmt.Errorf("clone for propose: %w", err)
	}
	wt, err := repo.Worktree()
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
		return EditResult{}, errors.New("changeset produced no changes vs base")
	}

	// Author = the k8s user who proposed; committer = dotvirt (the SA pushing).
	if _, err := wt.Commit(message, &git.CommitOptions{Author: by.signature(), Committer: author}); err != nil {
		return EditResult{}, fmt.Errorf("commit: %w", err)
	}

	res := EditResult{Branch: branch}
	if w.push {
		err := repo.Push(&git.PushOptions{
			Auth:     w.auth,
			RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/heads/%s", branch, branch))},
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return EditResult{}, fmt.Errorf("push %s: %w", branch, err)
		}
		res.Pushed = true
	}
	return res, nil
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
