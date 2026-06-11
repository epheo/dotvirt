// Package git is dotvirt's git plane: it clones a repo once into memory and
// reads VM manifests from any branch's tree without checking it out. Reads never
// touch the network — a single background fetcher (driven by the git poll, and
// nudged after dotvirt's own pushes) owns freshness by calling Refresh.
package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

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

// fetchTimeout bounds a single background fetch. Without it a stalled connection
// to the remote (common over a flaky/self-signed Route) would hold r.mu forever —
// and since reads (VMManifests) also take r.mu, every inventory read for that repo
// would deadlock. Capping the fetch lets it fail and release the lock; the poll
// retries on the next tick.
const fetchTimeout = 30 * time.Second

// Refresh updates the cached clone's branch refs from the remote. This is the
// single point of network freshness: the background fetcher calls it on a
// schedule, and it's nudged after dotvirt's own pushes. Reads never call it.
func (r *Repo) Refresh() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	err := r.repo.FetchContext(ctx, &git.FetchOptions{
		Auth:     r.auth,
		RefSpecs: []config.RefSpec{"+refs/heads/*:refs/heads/*"},
		Force:    true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch: %w", err)
	}
	return nil
}

// HeadsSignature refreshes from the remote and returns a stable string
// summarizing all branch heads (name+hash). A change means some branch moved.
// This is the background fetcher's entry point: it both fetches (the single
// network refresh) and reports whether anything changed, so the poll loop can
// push fresh inventory to subscribers.
func (r *Repo) HeadsSignature() (string, error) {
	if err := r.Refresh(); err != nil {
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
