package git

import (
	"errors"
	"fmt"
	"strings"
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

// WriteRepo is a worktree-backed clone used for committing: the running-branch
// export (export.Exporter) and the proposed-branch changesets (changeset
// CommitChangeset). Separate from the read-only mirror Repo so writes never
// disturb inventory reads.
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

// dotvirtSig is the signature dotvirt commits as for its OWN writes (the running-
// branch export, legacy single edits) and as the committer of user proposals (the
// SA that pushes). Built per call so the time is real — git history shows when a
// change actually landed. Idempotency never depended on a fixed time: the export
// skips a clean tree (Commit), and a changeset errors on no-op-vs-base
// (CommitChangeset); a fixed epoch only made identical re-proposes a push no-op,
// which isn't worth misdating every commit to 1970.
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
// the export owns its branch outright, and a re-propose rebuilds its branch fresh.
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

// Commit writes files onto branch and prunes stale ones: any tracked file under
// a managedDir that is NOT in files is deleted, so the branch reflects exactly
// the supplied set within those directories (a VM deleted from the cluster has
// its manifest removed). Files outside managedDirs (e.g. a README) are left
// alone. If the resulting tree is identical to the branch head, it commits
// nothing and returns Committed=false — keeping the running branch from churning.
//
// branch is created from the default branch if it doesn't exist yet (needed for
// feature branches; the running branch is expected to exist).
func (w *WriteRepo) Commit(branch, message string, files []File, managedDirs []string) (CommitResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	repo, wt, err := w.openWorktree()
	if err != nil {
		return CommitResult{}, err
	}

	if err := checkoutBranch(repo, wt, branch); err != nil {
		return CommitResult{}, err
	}

	keep := make(map[string]struct{}, len(files))
	for _, f := range files {
		keep[f.Path] = struct{}{}
		if err := writeWorktreeFile(wt, f); err != nil {
			return CommitResult{}, err
		}
		// Stage explicitly: Commit{All:true} only stages already-tracked files,
		// so newly created manifests would otherwise be left out.
		if _, err := wt.Add(f.Path); err != nil {
			return CommitResult{}, fmt.Errorf("stage %s: %w", f.Path, err)
		}
	}

	if err := pruneStale(repo, wt, keep, managedDirs); err != nil {
		return CommitResult{}, err
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

// pruneStale removes tracked files that live under a managedDir but are absent
// from keep — the deletions needed so the branch mirrors exactly the supplied set
// within those directories. It walks the branch HEAD's tree (not worktree status,
// which omits unmodified files — a stale VM manifest that isn't being rewritten
// must still be deleted). A managedDir matches a file if the file equals it or
// sits beneath it ("ns" matches "ns/vm.yaml"). No managedDirs means no pruning.
func pruneStale(repo *git.Repository, wt *git.Worktree, keep map[string]struct{}, managedDirs []string) error {
	if len(managedDirs) == 0 {
		return nil
	}
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
	var stale []string
	if err := tree.Files().ForEach(func(f *object.File) error {
		if _, ok := keep[f.Name]; ok {
			return nil
		}
		if underAny(f.Name, managedDirs) {
			stale = append(stale, f.Name)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, path := range stale {
		if _, err := wt.Remove(path); err != nil {
			return fmt.Errorf("prune %s: %w", path, err)
		}
	}
	return nil
}

// underAny reports whether path is one of, or nested under, any dir in dirs.
func underAny(path string, dirs []string) bool {
	for _, d := range dirs {
		if path == d || strings.HasPrefix(path, d+"/") {
			return true
		}
	}
	return false
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
