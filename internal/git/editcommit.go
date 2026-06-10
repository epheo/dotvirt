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
)

// EditResult reports the outcome of a VM edit committed to a feature branch.
type EditResult struct {
	Branch string `json:"branch"` // feature branch the edit landed on
	File   string `json:"file"`   // manifest path edited
	Hash   string `json:"hash"`   // commit hash
	Diff   string `json:"diff"`   // unified diff of the change
	Pushed bool   `json:"pushed"` // whether it was pushed to the remote
}

// CommitVMEdit edits a VM's manifest on the source branch and commits the result
// to featureBranch (created off source). It reads, edits, and commits within a
// single clone so the edit is based on current source state. Returns the diff.
//
// sourceFile is the manifest's repo-relative path (from the inventory). The edit
// is applied with ApplyEdit, so only the changed lines differ.
func (w *WriteRepo) CommitVMEdit(source, featureBranch, sourceFile, namespace, name, message string, edit VMEdit) (EditResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	repo, err := git.Clone(memory.NewStorage(), memfs.New(), &git.CloneOptions{URL: w.url, Auth: w.auth})
	if err != nil {
		return EditResult{}, fmt.Errorf("clone for edit: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return EditResult{}, err
	}

	// Start from the source branch's state.
	if err := checkoutBranch(repo, wt, source); err != nil {
		return EditResult{}, fmt.Errorf("checkout source %q: %w", source, err)
	}

	original, err := readWorktree(wt, sourceFile)
	if err != nil {
		return EditResult{}, fmt.Errorf("read %s on %s: %w", sourceFile, source, err)
	}

	edited, err := ApplyEdit(original, namespace, name, edit)
	if err != nil {
		return EditResult{}, err
	}

	// Move onto the feature branch (created at the source commit) before writing.
	if err := createFeatureBranch(repo, wt, featureBranch); err != nil {
		return EditResult{}, err
	}

	if err := writeWorktreeFile(wt, File{Path: sourceFile, Content: edited}); err != nil {
		return EditResult{}, err
	}
	if _, err := wt.Add(sourceFile); err != nil {
		return EditResult{}, fmt.Errorf("stage %s: %w", sourceFile, err)
	}

	status, err := wt.Status()
	if err != nil {
		return EditResult{}, err
	}
	if status.IsClean() {
		return EditResult{}, errors.New("edit produced no change")
	}

	commit, err := wt.Commit(message, &git.CommitOptions{Author: author, Committer: author})
	if err != nil {
		return EditResult{}, fmt.Errorf("commit: %w", err)
	}

	res := EditResult{
		Branch: featureBranch,
		File:   sourceFile,
		Hash:   commit.String(),
		Diff:   unifiedDiff(sourceFile, original, edited),
	}

	if w.push {
		err := repo.Push(&git.PushOptions{
			Auth:     w.auth,
			RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/heads/%s", featureBranch, featureBranch))},
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return EditResult{}, fmt.Errorf("push %s: %w", featureBranch, err)
		}
		res.Pushed = true
	}
	return res, nil
}

// CommitNewFile writes a new file (e.g. a generated VM manifest) onto a feature
// branch created off source, and commits it. It errors if the path already
// exists, to avoid silently overwriting. Returns the diff (full file as added).
func (w *WriteRepo) CommitNewFile(source, featureBranch, path, message string, content []byte) (EditResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	repo, err := git.Clone(memory.NewStorage(), memfs.New(), &git.CloneOptions{URL: w.url, Auth: w.auth})
	if err != nil {
		return EditResult{}, fmt.Errorf("clone for create: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return EditResult{}, err
	}
	if err := checkoutBranch(repo, wt, source); err != nil {
		return EditResult{}, fmt.Errorf("checkout source %q: %w", source, err)
	}

	if _, err := wt.Filesystem.Stat(path); err == nil {
		return EditResult{}, fmt.Errorf("%s already exists on %s", path, source)
	}

	if err := createFeatureBranch(repo, wt, featureBranch); err != nil {
		return EditResult{}, err
	}
	if err := writeWorktreeFile(wt, File{Path: path, Content: content}); err != nil {
		return EditResult{}, err
	}
	if _, err := wt.Add(path); err != nil {
		return EditResult{}, fmt.Errorf("stage %s: %w", path, err)
	}

	commit, err := wt.Commit(message, &git.CommitOptions{Author: author, Committer: author})
	if err != nil {
		return EditResult{}, fmt.Errorf("commit: %w", err)
	}

	res := EditResult{
		Branch: featureBranch,
		File:   path,
		Hash:   commit.String(),
		Diff:   unifiedDiff(path, nil, content),
	}
	if w.push {
		err := repo.Push(&git.PushOptions{
			Auth:     w.auth,
			RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/heads/%s", featureBranch, featureBranch))},
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return EditResult{}, fmt.Errorf("push %s: %w", featureBranch, err)
		}
		res.Pushed = true
	}
	return res, nil
}

// createFeatureBranch creates and checks out branch at the current HEAD commit.
func createFeatureBranch(repo *git.Repository, wt *git.Worktree, branch string) error {
	ref := plumbing.NewBranchReferenceName(branch)
	if _, err := repo.Reference(ref, true); err == nil {
		// Already exists: just check it out (e.g. iterating on the same edit branch).
		return wt.Checkout(&git.CheckoutOptions{Branch: ref})
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
