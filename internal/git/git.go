// Package git is dotvirt's git plane: it clones a repo once into memory and
// reads manifests from any branch's tree without checking it out. Reads never
// touch the network - a single background fetcher (driven by the git poll, and
// nudged after dotvirt's own pushes) owns freshness.
package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/forge"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
)

// creds is the shared https identity of the read and write repos. auth is
// rebuilt per call so a rotated token takes effect without restart; nil when no
// token yet, so go-git treats the remote as public.
type creds struct {
	url      string
	username string
	tokenFn  forge.TokenSource
}

func (c creds) auth() *http.BasicAuth {
	if c.tokenFn == nil {
		return nil
	}
	tok := c.tokenFn()
	if tok == "" {
		return nil
	}
	return &http.BasicAuth{Username: c.username, Password: tok}
}

// Repo is a cached clone of the manifest repository. Reads are concurrency-safe.
type Repo struct {
	creds

	// fetchMu serializes refresh; reads never take it, so a slow fetch
	// cannot stall them.
	fetchMu sync.Mutex

	mu   sync.Mutex
	repo *git.Repository

	memos memoTable
}

// Open clones url's branches into memory (bare) and returns a Repo. username +
// tokenFn provide https basic auth, resolved on each fetch (pass a nil/empty
// source for public/local).
func Open(url, username string, tokenFn forge.TokenSource) (*Repo, error) {
	r := &Repo{creds: creds{url: url, username: username, tokenFn: tokenFn}}
	if err := r.clone(); err != nil {
		return nil, err
	}
	return r, nil
}

// URL is the remote this mirror reads, for logs.
func (r *Repo) URL() string { return r.url }

// branchHash returns branch's current commit hash from the local mirror (no fetch),
// or "" if the branch can't be resolved - the memo invalidation key.
func (r *Repo) branchHash(branch string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, err := r.repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return ""
	}
	return ref.Hash().String()
}

// headRefs maps the remote's branches onto local branch refs, the only refs
// reads resolve. A mirror clone would also pull the forge's refs/pull/*, one
// per PR ever opened, and never move them again.
var headRefs = []config.RefSpec{"+refs/heads/*:refs/heads/*"}

func (r *Repo) clone() error {
	ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
	defer cancel()
	repo, err := git.Init(memory.NewStorage(), nil)
	if err == nil {
		_, err = repo.CreateRemote(&config.RemoteConfig{Name: git.DefaultRemoteName, URLs: []string{r.url}, Fetch: headRefs})
	}
	if err == nil {
		err = r.fetchHeads(ctx, repo)
	}
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("clone %s: %w", r.url, err)
	}
	r.repo = repo
	return nil
}

// fetchHeads brings repo's branches level with the remote's, dropping the
// ones deleted there so a merged PR's branch does not outlive it.
func (r *Repo) fetchHeads(ctx context.Context, repo *git.Repository) error {
	return repo.FetchContext(ctx, &git.FetchOptions{
		Auth:     r.auth(),
		RefSpecs: headRefs,
		Force:    true,
		Prune:    true,
	})
}

// Network bounds: a stalled remote (common over a flaky or self-signed Route)
// must fail and let the next poll or request retry, never hang a goroutine.
const (
	cloneTimeout = 2 * time.Minute
	fetchTimeout = 30 * time.Second
)

// refresh updates the cached clone's branch refs from the remote. This is the
// single point of network freshness: the background fetcher calls it on a
// schedule, and it's nudged after dotvirt's own pushes. Reads never call it.
//
// The fetch writes into a fork of the mirror's storage and swaps it in under
// r.mu: go-git's memory storage is unsynchronized maps, so fetching into the
// live one would race the reads, and locking it would stall them on the network.
func (r *Repo) refresh() error {
	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()

	r.mu.Lock()
	fork, err := forkStorage(r.repo.Storer)
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	next, err := git.Open(fork, nil)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	err = r.fetchHeads(ctx, next)
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	r.mu.Lock()
	r.repo = next
	r.mu.Unlock()
	return nil
}

// forkStorage copies the mirror's indexes; objects are immutable and shared,
// so a fetch into the fork leaves the original untouched.
func forkStorage(s storage.Storer) (*memory.Storage, error) {
	src, ok := s.(*memory.Storage)
	if !ok {
		return nil, fmt.Errorf("mirror storage is %T, not memory", s)
	}
	dst := memory.NewStorage()
	dst.ConfigStorage = src.ConfigStorage
	maps.Copy(dst.Objects, src.Objects)
	maps.Copy(dst.Commits, src.Commits)
	maps.Copy(dst.Trees, src.Trees)
	maps.Copy(dst.Blobs, src.Blobs)
	maps.Copy(dst.Tags, src.Tags)
	maps.Copy(dst.ReferenceStorage, src.ReferenceStorage)
	dst.ShallowStorage = slices.Clone(src.ShallowStorage)
	return dst, nil
}

// headsSignature refreshes from the remote and returns a stable string
// summarizing all branch heads (name+hash). A change means some branch moved.
// This is the background fetcher's entry point: it both fetches (the single
// network refresh) and reports whether anything changed, so the poll loop can
// push fresh inventory to subscribers.
func (r *Repo) headsSignature() (string, error) {
	if err := r.refresh(); err != nil {
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

// WalkYAML visits branch's .yaml/.yml files passing include: the one tree
// walk behind every per-branch index. visit runs under the repo lock, so it
// must not call back into r.
func (r *Repo) WalkYAML(branch string, include func(path string) bool, visit func(path string, content []byte) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tree, err := r.treeFor(branch)
	if err != nil {
		return err
	}
	return tree.Files().ForEach(func(f *object.File) error {
		if !isYAML(f.Name) || !include(f.Name) {
			return nil
		}
		content, err := readFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f.Name, err)
		}
		return visit(f.Name, content)
	})
}

// LookupOnBranch is FileOnBranch distinguishing absence from failure: ok=false
// with a nil error means the branch resolves but holds no such file - the
// release path branches on exactly that.
func (r *Repo) LookupOnBranch(branch, path string) (content []byte, ok bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tree, err := r.treeFor(branch)
	if err != nil {
		return nil, false, err
	}
	f, err := tree.File(path)
	if errors.Is(err, object.ErrFileNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("file %s on %s: %w", path, branch, err)
	}
	b, err := readFile(f)
	return b, err == nil, err
}

// Pins is each of paths' blob ID on branch, AbsentBlob for a path the
// branch does not hold: the versions whole-file content is made from.
func (r *Repo) Pins(branch string, paths ...string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tree, err := r.treeFor(branch)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		if out[p], err = pinIn(tree, p); err != nil {
			return nil, fmt.Errorf("file %s on %s: %w", p, branch, err)
		}
	}
	return out, nil
}

// FileOnBranch returns one file's raw content on branch - e.g. a base-branch
// manifest for the raw-YAML view.
func (r *Repo) FileOnBranch(branch, path string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tree, err := r.treeFor(branch)
	if err != nil {
		return nil, err
	}
	f, err := tree.File(path)
	if err != nil {
		return nil, fmt.Errorf("file %s on %s: %w", path, branch, err)
	}
	return readFile(f)
}

// treeFor is branch's head tree. Caller holds r.mu.
func (r *Repo) treeFor(branch string) (*object.Tree, error) {
	c, err := r.branchCommit(branch)
	if err != nil {
		return nil, err
	}
	return c.Tree()
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

// InTemplatesDir: the library holds blueprints, not declared objects (a
// template's embedded VM is not inventory); TemplatesOnBranch reads only it.
func InTemplatesDir(name string) bool {
	return strings.HasPrefix(name, model.TemplatesDir+"/")
}

// TemplatesOnBranch returns every .yaml file under templates/ on branch - the
// repo's template library, parsed by the caller.
func (r *Repo) TemplatesOnBranch(branch string) ([]model.File, error) {
	var out []model.File
	err := r.WalkYAML(branch, InTemplatesDir, func(path string, content []byte) error {
		out = append(out, model.File{Path: path, Content: content})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
