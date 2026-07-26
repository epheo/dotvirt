package git

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/epheo/dotvirt/pkg/forge"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

// WriteRepo is a worktree-backed clone used for committing the proposed-branch
// changesets (changeset CommitChangeset). Separate from the read-only mirror
// Repo so writes never disturb inventory reads.
type WriteRepo struct {
	url      string
	username string
	tokenFn  forge.TokenSource // resolved per clone/push so a rotated token is picked up
	push     bool              // push commits to the remote (false for local/offline testing)

	mu sync.Mutex
}

// OpenWrite prepares a writable view of the repo. Cloning happens per-operation
// so each commit starts from fresh remote state. username + tokenFn provide basic
// auth, resolved on each clone/push. push controls whether commits are pushed back
// (set false when there's no writable remote, e.g. tests).
func OpenWrite(url, username string, tokenFn forge.TokenSource, push bool) *WriteRepo {
	return &WriteRepo{url: url, username: username, tokenFn: tokenFn, push: push}
}

// auth builds a fresh BasicAuth from the current token (nil when no token yet).
// Rebuilt per call so a rotated token takes effect without restart.
func (w *WriteRepo) auth() *http.BasicAuth {
	if w.tokenFn == nil {
		return nil
	}
	tok := w.tokenFn()
	if tok == "" {
		return nil
	}
	return &http.BasicAuth{Username: w.username, Password: tok}
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

// dotvirtSig is the signature for dotvirt's own writes (template seeding) and
// the committer of user proposals (the SA that pushes). Built per call so the
// time is real: git history shows when a change actually landed. A fixed epoch
// would only make identical re-proposes a push no-op, not worth misdating
// every commit.
func dotvirtSig() *object.Signature {
	return &object.Signature{Name: "dotvirt", Email: "dotvirt@localhost", When: time.Now().UTC()}
}

// Author identifies who a change is attributed to (the k8s user proposing it).
// The committer stays dotvirt (the SA that pushes); git separates the two.
type Author struct {
	Name  string
	Email string
}

// signature builds the commit author signature: the user when given, else dotvirt.
func (a Author) signature() *object.Signature {
	if a.Name == "" {
		return dotvirtSig()
	}
	email := a.Email
	if email == "" {
		email = "dotvirt@localhost"
	}
	return &object.Signature{Name: a.Name, Email: email, When: time.Now().UTC()}
}

// openWorktree clones the repo into memory and returns it with its worktree —
// every write operation starts from fresh remote state. Callers hold w.mu.
func (w *WriteRepo) openWorktree() (*git.Repository, *git.Worktree, error) {
	repo, err := git.Clone(memory.NewStorage(), memfs.New(), &git.CloneOptions{
		URL:  w.url,
		Auth: w.auth(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("clone for write: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, nil, err
	}
	return repo, wt, nil
}

// pushBranch force-pushes branch when pushes are enabled (no-op otherwise); an
// already-up-to-date remote is not an error. Force is correct for both callers:
// the template seed lands on a repo dotvirt just created, and a re-propose
// rebuilds its branch fresh.
func (w *WriteRepo) pushBranch(repo *git.Repository, branch string) error {
	if !w.push {
		return nil
	}
	err := repo.Push(&git.PushOptions{
		Auth:     w.auth(),
		RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/heads/%s", branch, branch))},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("push %s: %w", branch, err)
	}
	return nil
}

// Commit writes files onto branch. If the resulting tree is identical to the
// branch head, it commits nothing and returns Committed=false, so a no-op
// never churns history.
//
// branch is created from the default branch if it doesn't exist yet.
func (w *WriteRepo) Commit(branch, message string, files []File) (CommitResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	repo, wt, err := w.openWorktree()
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

	sig := dotvirtSig()
	commit, err := wt.Commit(message, &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		return CommitResult{}, fmt.Errorf("commit: %w", err)
	}

	if err := w.pushBranch(repo, branch); err != nil {
		return CommitResult{}, err
	}

	return CommitResult{Branch: branch, Committed: true, Hash: commit.String()}, nil
}

// checkoutBranch checks out branch as a local branch tracking the remote. A
// fresh clone only materializes the default branch locally; every other branch
// exists only as a remote-tracking ref (refs/remotes/origin/<branch>). So we
// resolve that remote ref and create the local branch at the same commit,
// ensuring we build on the branch's real remote state rather than re-forking it
// from HEAD each time (which would discard the branch's prior commits).
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
	if _, err := file.Write(f.Content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", f.Path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", f.Path, err)
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
