// Package git is dotvirt's git plane: it clones a repo once and reads VM
// manifests from any branch's tree without checking it out, so inventory reads
// never disturb a working copy.
package git

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Repo is a cached clone of the manifest repository. Reads are concurrency-safe.
type Repo struct {
	url  string
	auth *http.BasicAuth

	mu   sync.Mutex
	repo *git.Repository
}

// Open clones url into memory (bare, all branches) and returns a Repo. username
// and token are used for https basic auth; pass empty strings for public/local.
func Open(url, username, token string) (*Repo, error) {
	r := &Repo{url: url}
	if token != "" {
		r.auth = &http.BasicAuth{Username: username, Password: token}
	}
	if err := r.clone(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Repo) clone() error {
	repo, err := git.Clone(memory.NewStorage(), nil, &git.CloneOptions{
		URL:          r.url,
		Auth:         r.auth,
		Mirror:       true, // fetch all refs; we read branches directly from refs
		SingleBranch: false,
	})
	if err != nil {
		return fmt.Errorf("clone %s: %w", r.url, err)
	}
	r.repo = repo
	return nil
}

// Fetch updates all remote refs. Safe to call before a read to get fresh state.
func (r *Repo) Fetch() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.repo.Fetch(&git.FetchOptions{
		Auth:     r.auth,
		RefSpecs: []config.RefSpec{"+refs/heads/*:refs/heads/*"},
		Force:    true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch: %w", err)
	}
	return nil
}

// Branches lists local branch names (a mirror clone stores remote heads as
// local heads), sorted, with the conventional default first.
func (r *Repo) Branches() ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	iter, err := r.repo.Branches()
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}
	var names []string
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		names = append(names, ref.Name().Short())
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// HeadsSignature fetches the remote and returns a stable string summarizing all
// branch heads (name+hash). A change in this signature means some branch moved —
// used by the live poll to know when to push fresh inventory.
func (r *Repo) HeadsSignature() (string, error) {
	if err := r.Fetch(); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	iter, err := r.repo.Branches()
	if err != nil {
		return "", err
	}
	var heads []string
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		heads = append(heads, ref.Name().Short()+"="+ref.Hash().String())
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(heads)
	return strings.Join(heads, ","), nil
}

// ManifestFile is one VM manifest located in the repo.
type ManifestFile struct {
	Path    string // path within the repo
	Content []byte
}

// VMManifests returns every file on branch that contains a VirtualMachine doc.
// Files are matched by .yaml/.yml extension then filtered by content, so a
// single file with multiple docs is still found.
func (r *Repo) VMManifests(branch string) ([]ManifestFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tree, err := r.treeFor(branch)
	if err != nil {
		return nil, err
	}

	var out []ManifestFile
	err = tree.Files().ForEach(func(f *object.File) error {
		if !isYAML(f.Name) {
			return nil
		}
		content, err := readFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f.Name, err)
		}
		if !containsVirtualMachine(content) {
			return nil
		}
		out = append(out, ManifestFile{Path: f.Name, Content: content})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// treeFor resolves branch -> commit -> tree. Caller holds r.mu.
func (r *Repo) treeFor(branch string) (*object.Tree, error) {
	ref, err := r.repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return nil, fmt.Errorf("resolve branch %q: %w", branch, err)
	}
	commit, err := r.repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("commit for %q: %w", branch, err)
	}
	return commit.Tree()
}

func readFile(f *object.File) ([]byte, error) {
	rc, err := f.Reader()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// containsVirtualMachine is a cheap pre-filter; full parsing happens later.
func containsVirtualMachine(content []byte) bool {
	return strings.Contains(string(content), "kind: VirtualMachine")
}
