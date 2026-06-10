package git

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

// WriteRepo is a worktree-backed clone used for committing: the running-branch
// export (cluster.Exporter) and feature-branch edits (Slice 4). Separate from
// the read-only mirror Repo so writes never disturb inventory reads.
type WriteRepo struct {
	url  string
	auth *http.BasicAuth
	push bool // push commits to the remote (false for local/offline testing)

	mu sync.Mutex
}

// OpenWrite prepares a writable view of the repo. Cloning happens per-operation
// so each commit starts from fresh remote state. push controls whether commits
// are pushed back (set false when there's no writable remote, e.g. tests).
func OpenWrite(url, username, token string, push bool) *WriteRepo {
	w := &WriteRepo{url: url, push: push}
	if token != "" {
		w.auth = &http.BasicAuth{Username: username, Password: token}
	}
	return w
}

// File is a path/content pair to write into the repo.
type File struct {
	Path    string
	Content []byte
}

// CommitResult reports what a commit did.
type CommitResult struct {
	Branch    string
	Committed bool   // false when the tree was already up to date (no-op)
	Hash      string // commit hash when Committed
}

// author is the identity dotvirt commits as.
var author = &object.Signature{Name: "dotvirt", Email: "dotvirt@localhost", When: time.Unix(0, 0).UTC()}

// Commit writes files onto branch and commits them. If the resulting tree is
// identical to the branch head (no content changed), it commits nothing and
// returns Committed=false — this keeps the running branch from churning when the
// cluster hasn't changed.
//
// branch is created from the default branch if it doesn't exist yet (needed for
// feature branches; the running branch is expected to exist).
func (w *WriteRepo) Commit(branch, message string, files []File) (CommitResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	repo, err := git.Clone(memory.NewStorage(), memfs.New(), &git.CloneOptions{
		URL:  w.url,
		Auth: w.auth,
	})
	if err != nil {
		return CommitResult{}, fmt.Errorf("clone for write: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return CommitResult{}, err
	}

	if err := checkoutBranch(repo, wt, branch); err != nil {
		return CommitResult{}, err
	}

	for _, f := range files {
		if err := writeWorktreeFile(wt, f); err != nil {
			return CommitResult{}, err
		}
		// Stage explicitly: Commit{All:true} only stages already-tracked files,
		// so newly created manifests would otherwise be left out.
		if _, err := wt.Add(f.Path); err != nil {
			return CommitResult{}, fmt.Errorf("stage %s: %w", f.Path, err)
		}
	}

	status, err := wt.Status()
	if err != nil {
		return CommitResult{}, err
	}
	if status.IsClean() {
		return CommitResult{Branch: branch, Committed: false}, nil
	}

	commit, err := wt.Commit(message, &git.CommitOptions{Author: author, Committer: author})
	if err != nil {
		return CommitResult{}, fmt.Errorf("commit: %w", err)
	}

	if w.push {
		err := repo.Push(&git.PushOptions{
			Auth:     w.auth,
			RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/heads/%s", branch, branch))},
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return CommitResult{}, fmt.Errorf("push %s: %w", branch, err)
		}
	}

	return CommitResult{Branch: branch, Committed: true, Hash: commit.String()}, nil
}

// checkoutBranch checks out branch as a local branch tracking the remote. A
// fresh clone only materializes the default branch locally; every other branch
// exists only as a remote-tracking ref (refs/remotes/origin/<branch>). So we
// resolve that remote ref and create the local branch at the same commit,
// ensuring we build on the branch's real remote state rather than re-forking it
// from HEAD each time (which would discard prior commits — e.g. running export).
func checkoutBranch(repo *git.Repository, wt *git.Worktree, branch string) error {
	local := plumbing.NewBranchReferenceName(branch)
	if _, err := repo.Reference(local, true); err == nil {
		return wt.Checkout(&git.CheckoutOptions{Branch: local})
	}

	remoteRef := plumbing.NewRemoteReferenceName("origin", branch)
	if rr, err := repo.Reference(remoteRef, true); err == nil {
		// Create local branch at the remote branch's commit.
		if err := repo.Storer.SetReference(plumbing.NewHashReference(local, rr.Hash())); err != nil {
			return err
		}
		return wt.Checkout(&git.CheckoutOptions{Branch: local})
	}

	// Branch exists nowhere yet: create it from current HEAD (new feature branch).
	return wt.Checkout(&git.CheckoutOptions{Branch: local, Create: true})
}

func writeWorktreeFile(wt *git.Worktree, f File) error {
	if err := ensureDir(wt, f.Path); err != nil {
		return err
	}
	file, err := wt.Filesystem.Create(f.Path)
	if err != nil {
		return fmt.Errorf("create %s: %w", f.Path, err)
	}
	defer file.Close()
	if _, err := file.Write(f.Content); err != nil {
		return fmt.Errorf("write %s: %w", f.Path, err)
	}
	return nil
}

func ensureDir(wt *git.Worktree, path string) error {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return wt.Filesystem.MkdirAll(path[:i], 0o755)
		}
	}
	return nil
}
