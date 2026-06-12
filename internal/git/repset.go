package git

import (
	"context"
	"sync"
	"time"
)

// RepoSet manages dotvirt's per-project git repositories: one read mirror + one
// writable view per repo URL, opened lazily on first use and cached. All repos
// share the single Forgejo credential (the only non-cluster input); which repo a
// caller may touch is gated upstream by the project resolver, not here.
//
// Each opened repo gets one background poll goroutine that drives the
// single-fetcher freshness model (Repo.HeadsSignature does the only network
// fetch) and signals a shared change channel when that repo's heads move — so the
// inventory hub recomputes. This generalizes main's former single pollGit.
type RepoSet struct {
	user  string
	token string
	push  bool

	// Poll wiring, shared by every repo's poll goroutine.
	ctx        context.Context
	changed    chan<- struct{} // the process-wide inventory bus (hub recomputes)
	gitChanged chan<- struct{} // git-only signal (proposals refresher re-queries the forge)
	interval   time.Duration

	mu    sync.Mutex
	cache map[string]*repoPair
}

type repoPair struct {
	read  *Repo
	write *WriteRepo
}

// NewRepoSet builds a RepoSet. forgeUser/forgeToken authenticate every repo's
// clone/push; push=false disables pushes (offline/tests). ctx bounds the
// per-repo poll goroutines. Both channels receive a coalesced signal whenever any
// repo's branch heads move: changed is the process-wide inventory bus, gitChanged
// the git-only side (the proposals refresher, which must not wake on the cluster
// events the bus also carries). Either may be nil to disable that signal.
func NewRepoSet(ctx context.Context, forgeUser, forgeToken string, push bool, changed, gitChanged chan<- struct{}, interval time.Duration) *RepoSet {
	return &RepoSet{
		user:       forgeUser,
		token:      forgeToken,
		push:       push,
		ctx:        ctx,
		changed:    changed,
		gitChanged: gitChanged,
		interval:   interval,
		cache:      map[string]*repoPair{},
	}
}

// Get returns the read mirror and writable view for repoURL, opening them on
// first call (and starting that repo's background poll). Subsequent calls return
// the cached pair. The read clone can fail (network/auth/bad URL); the write view
// is lazy (clones per commit) so OpenWrite itself never errors.
func (s *RepoSet) Get(repoURL string) (*Repo, *WriteRepo, error) {
	s.mu.Lock()
	if p, ok := s.cache[repoURL]; ok {
		s.mu.Unlock()
		return p.read, p.write, nil
	}
	s.mu.Unlock()

	// Open outside the lock: the read clone hits the network and can be slow; we
	// don't want to block Get for other repos. A rare duplicate open under a race
	// is resolved below (first writer wins, later opens are discarded).
	read, err := Open(repoURL, s.user, s.token)
	if err != nil {
		return nil, nil, err
	}
	write := OpenWrite(repoURL, s.user, s.token, s.push)

	s.mu.Lock()
	if p, ok := s.cache[repoURL]; ok {
		s.mu.Unlock()
		return p.read, p.write, nil // someone else won the race; use theirs
	}
	pair := &repoPair{read: read, write: write}
	s.cache[repoURL] = pair
	s.mu.Unlock()

	go s.poll(read)
	return pair.read, pair.write, nil
}

// poll fetches repo on an interval and signals changed when its heads move. Like
// main's former pollGit but one goroutine per open repo; HeadsSignature is the
// single network fetch (reads never fetch).
func (s *RepoSet) poll(repo *Repo) {
	last := ""
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			sig, err := repo.HeadsSignature()
			if err != nil {
				continue
			}
			if sig != last {
				last = sig
				signal(s.ctx, s.changed)
				signal(s.ctx, s.gitChanged)
			}
		}
	}
}

// signal sends a coalesced change notification, dropping it if one is already
// pending, ctx is done, or the channel is nil.
func signal(ctx context.Context, changed chan<- struct{}) {
	select {
	case changed <- struct{}{}:
	case <-ctx.Done():
	default:
	}
}
