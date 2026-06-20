package git

import (
	"context"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/pkg/forge"
)

// RepoSet manages dotvirt's per-project git repositories: one read mirror + one
// writable view per repo URL, opened lazily on first use and cached. All repos
// share the single Forgejo credential (the only non-cluster input); which repo a
// caller may touch is gated upstream by the project resolver, not here.
//
// Each opened repo gets one background poll goroutine that drives the
// single-fetcher freshness model (Repo.HeadsSignature does the only network
// fetch) and publishes GitChanged on the shared event bus when that repo's heads
// move — so the inventory hub recomputes and the proposals refresher re-queries the
// forge. The poll is the missed-event backstop; the forge webhook (via Poke) is the
// primary, near-instant trigger.
type RepoSet struct {
	user    string
	tokenFn forge.TokenSource
	push    bool

	// Poll wiring, shared by every repo's poll goroutine.
	ctx      context.Context
	bus      *eventbus.Bus // publishes GitChanged on a head move
	interval time.Duration

	mu sync.Mutex
	// cache is keyed by the CANONICAL repo URL (forge.NormalizeRepoURL), so the same
	// repo written three ways (clone_url with .git, html_url without, a trailing-slash
	// annotation) resolves to one entry — a webhook Poke can't miss it on a spelling
	// difference. The raw URL each entry was opened with is used for the clone.
	cache map[string]*repoPair
}

type repoPair struct {
	read  *Repo
	write *WriteRepo
	poke  chan struct{} // out-of-cycle fetch trigger (webhook), coalesced
}

// NewRepoSet builds a RepoSet. forgeUser/forgeToken authenticate every repo's
// clone/push; push=false disables pushes (offline/tests). ctx bounds the per-repo
// poll goroutines. bus receives a coalesced GitChanged whenever any repo's branch
// heads move (subscribers: the inventory hub and the proposals refresher); it may
// be nil to disable signalling (tests).
func NewRepoSet(ctx context.Context, forgeUser string, tokenFn forge.TokenSource, push bool, bus *eventbus.Bus, interval time.Duration) *RepoSet {
	return &RepoSet{
		user:     forgeUser,
		tokenFn:  tokenFn,
		push:     push,
		ctx:      ctx,
		bus:      bus,
		interval: interval,
		cache:    map[string]*repoPair{},
	}
}

// Get returns the read mirror and writable view for repoURL, opening them on
// first call (and starting that repo's background poll). Subsequent calls return
// the cached pair. The read clone can fail (network/auth/bad URL); the write view
// is lazy (clones per commit) so OpenWrite itself never errors.
func (s *RepoSet) Get(repoURL string) (*Repo, *WriteRepo, error) {
	key := forge.NormalizeRepoURL(repoURL)
	s.mu.Lock()
	if p, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return p.read, p.write, nil
	}
	s.mu.Unlock()

	// Open outside the lock with the RAW url (the clone may need the exact form):
	// the read clone hits the network and can be slow; we don't want to block Get
	// for other repos. A rare duplicate open under a race is resolved below (first
	// writer wins, later opens are discarded).
	read, err := Open(repoURL, s.user, s.tokenFn)
	if err != nil {
		return nil, nil, err
	}
	write := OpenWrite(repoURL, s.user, s.tokenFn, s.push)

	s.mu.Lock()
	if p, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return p.read, p.write, nil // someone else won the race; use theirs
	}
	pair := &repoPair{read: read, write: write, poke: make(chan struct{}, 1)}
	s.cache[key] = pair
	s.mu.Unlock()

	go s.poll(pair)
	return pair.read, pair.write, nil
}

// Poke fetches repoURL's heads now instead of waiting for the next poll tick —
// the webhook's instant-update path. Matched by canonical URL, so a webhook
// payload's clone_url/html_url form finds the entry the project annotation opened.
// A repo not opened yet is ignored (the regular resolve→Get path will open and poll
// it). Nil-safe like the other optional collaborators.
func (s *RepoSet) Poke(repoURL string) {
	if s == nil {
		return
	}
	key := forge.NormalizeRepoURL(repoURL)
	s.mu.Lock()
	p := s.cache[key]
	s.mu.Unlock()
	if p == nil {
		return
	}
	select {
	case p.poke <- struct{}{}:
	default: // a poke is already pending
	}
}

// poll fetches repo on an interval — or immediately on a poke — and publishes
// GitChanged when its heads move. One goroutine per open repo; HeadsSignature is
// the single network fetch (reads never fetch). The ticker is the missed-event
// backstop; the poke (webhook) is the primary trigger.
func (s *RepoSet) poll(p *repoPair) {
	last := ""
	check := func() {
		sig, err := p.read.HeadsSignature()
		if err != nil || sig == last {
			return
		}
		last = sig
		s.bus.Publish(eventbus.GitChanged)
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			check()
		case <-p.poke:
			check()
		}
	}
}
