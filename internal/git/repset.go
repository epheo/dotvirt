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
	ctx      context.Context
	changed  chan<- struct{}
	onChange func() // git-specific hook (heads moved), distinct from the shared `changed`
	interval time.Duration

	mu    sync.Mutex
	cache map[string]*repoPair
}

type repoPair struct {
	read  *Repo
	write *WriteRepo
}

// NewRepoSet builds a RepoSet. forgeUser/forgeToken authenticate every repo's
// clone/push; push=false disables pushes (offline/tests). ctx bounds the
// per-repo poll goroutines; changed receives a signal whenever any repo's branch
// heads move; interval is the poll period.
func NewRepoSet(ctx context.Context, forgeUser, forgeToken string, push bool, changed chan<- struct{}, interval time.Duration) *RepoSet {
	return &RepoSet{
		user:     forgeUser,
		token:    forgeToken,
		push:     push,
		ctx:      ctx,
		changed:  changed,
		interval: interval,
		cache:    map[string]*repoPair{},
	}
}

// SetOnChange registers a callback fired whenever a repo's branch heads move (a
// push or merge). dotvirt flushes the per-token proposals cache here, so the open-PR
// lane refreshes on a real git change instead of re-polling the forge every
// heartbeat. Call once at startup, before any Get starts a poll goroutine.
func (s *RepoSet) SetOnChange(fn func()) { s.onChange = fn }

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
				if s.onChange != nil {
					s.onChange()
				}
			}
		}
	}
}

// signal sends a coalesced change notification, dropping it if one is already
// pending or ctx is done.
func signal(ctx context.Context, changed chan<- struct{}) {
	select {
	case changed <- struct{}{}:
	case <-ctx.Done():
	default:
	}
}
