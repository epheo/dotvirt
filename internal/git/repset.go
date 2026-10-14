package git

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/pkg/forge"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// RepoSet manages dotvirt's per-project git repositories: one read mirror + one
// writable view per repo URL, opened lazily on first use and cached. All repos
// share the single Forgejo credential (the only non-cluster input); which repo a
// caller may touch is gated upstream by the project resolver, not here.
//
// Each opened repo gets one background poll goroutine that drives the
// single-fetcher freshness model (Repo.headsSignature does the only network
// fetch) and publishes GitChanged on the shared event bus when that repo's heads
// move - so the inventory hub recomputes and the proposals refresher re-queries the
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
	// annotation) resolves to one entry - a webhook Poke can't miss it on a spelling
	// difference. The raw URL each entry was opened with is used for the clone.
	cache map[string]*repoPair
	// failed holds a recent Open error per key so a dead repo is retried after
	// openBackoff, not re-cloned by every inventory build.
	failed map[string]openFailure
	// opening lets concurrent Gets of one repo share a single clone.
	opening map[string]chan struct{}

	open func(url, user string, tokenFn forge.TokenSource) (*Repo, error)
	now  func() time.Time
}

type repoPair struct {
	key   string
	read  *Repo
	write *WriteRepo
	poke  chan struct{} // out-of-cycle fetch trigger (webhook), coalesced
	used  time.Time     // last Get; guarded by RepoSet.mu
}

type openFailure struct {
	err   error
	until time.Time
}

const (
	openBackoff = 30 * time.Second
	// idleTimeout evicts a repo no Get has asked for, e.g. a deleted project's:
	// its poll would otherwise fetch forever. A later Get re-clones it fresh.
	idleTimeout = time.Hour
)

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
		failed:   map[string]openFailure{},
		opening:  map[string]chan struct{}{},
		open:     Open,
		now:      time.Now,
	}
}

// Get returns the read mirror and writable view for repoURL, opening them on
// first call (and starting that repo's background poll). Subsequent calls return
// the cached pair. The read clone can fail (network/auth/bad URL); the write view
// is lazy (clones per commit) so OpenWrite itself never errors.
func (s *RepoSet) Get(repoURL string) (*Repo, *WriteRepo, error) {
	key := forge.NormalizeRepoURL(repoURL)
	var done chan struct{}
	for done == nil {
		s.mu.Lock()
		if p, ok := s.cache[key]; ok {
			p.used = s.now()
			s.mu.Unlock()
			return p.read, p.write, nil
		}
		if f, ok := s.failed[key]; ok && s.now().Before(f.until) {
			s.mu.Unlock()
			return nil, nil, f.err
		}
		if wait, ok := s.opening[key]; ok {
			s.mu.Unlock()
			<-wait
			continue
		}
		done = make(chan struct{})
		s.opening[key] = done
		s.mu.Unlock()
	}

	// Open outside the lock with the RAW url (the clone may need the exact form):
	// the read clone hits the network and can be slow; we don't want to block Get
	// for other repos.
	read, err := s.open(repoURL, s.user, s.tokenFn)

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.opening, key)
	close(done)
	if err != nil {
		s.failed[key] = openFailure{err: err, until: s.now().Add(openBackoff)}
		return nil, nil, err
	}
	delete(s.failed, key)
	pair := &repoPair{
		key:   key,
		read:  read,
		write: OpenWrite(repoURL, s.user, s.tokenFn, s.push),
		poke:  make(chan struct{}, 1),
		used:  s.now(),
	}
	s.cache[key] = pair
	go s.poll(pair)
	return pair.read, pair.write, nil
}

// Created clears repoURL's cached open failure: dotvirt just made the repo on
// the forge, so a "not found" from before must not fail the seed or the first
// propose for the rest of the backoff.
func (s *RepoSet) Created(repoURL string) {
	s.mu.Lock()
	delete(s.failed, forge.NormalizeRepoURL(repoURL))
	s.mu.Unlock()
}

// Poke fetches repoURL's heads now instead of waiting for the next poll tick -
// the webhook's instant-update path. Matched by canonical URL, so a webhook
// payload's clone_url/html_url form finds the entry the project annotation opened.
// A repo not opened yet is ignored (the regular resolve->Get path will open and poll
// it). Nil-safe like the other optional collaborators.
func (s *RepoSet) Poke(repoURL string) {
	if s == nil {
		return
	}
	key := forge.NormalizeRepoURL(repoURL)
	s.mu.Lock()
	p := s.cache[key]
	delete(s.failed, key) // the forge says the repo exists: retry its clone now
	s.mu.Unlock()
	if p == nil {
		return
	}
	select {
	case p.poke <- struct{}{}:
	default: // a poke is already pending
	}
}

// poll fetches repo on an interval - or immediately on a poke - and publishes
// GitChanged when its heads move. One goroutine per open repo; headsSignature is
// the single network fetch (reads never fetch). The ticker is the missed-event
// backstop; the poke (webhook) is the primary trigger. It ends, dropping the
// entry, once the repo is idle or gone from the forge.
func (s *RepoSet) poll(p *repoPair) {
	last := ""
	lastErr := ""
	check := func() (gone bool) {
		sig, err := p.read.headsSignature()
		if errors.Is(err, transport.ErrRepositoryNotFound) {
			log.Printf("git: poll %s: repo gone, dropping it", p.read.url)
			return true
		}
		if err != nil {
			// A failing fetch means the mirror is serving STALE git with no other
			// signal anywhere - the exact state a webhook-outage postmortem found
			// invisible. Log each distinct failure (and the recovery) instead of
			// swallowing it; the dedupe keeps a persistent outage to one line.
			if err.Error() != lastErr {
				lastErr = err.Error()
				log.Printf("git: poll %s: fetch failing (mirror stale until it recovers): %v", p.read.url, err)
			}
			return false
		}
		if lastErr != "" {
			log.Printf("git: poll %s: fetch recovered", p.read.url)
			lastErr = ""
		}
		if sig == last {
			return false
		}
		last = sig
		s.bus.PublishKey(eventbus.GitChanged, p.key)
		return false
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			if s.idle(p) || check() {
				s.evict(p)
				return
			}
		case <-p.poke:
			if check() {
				s.evict(p)
				return
			}
		}
	}
}

func (s *RepoSet) idle(p *repoPair) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now().Sub(p.used) > idleTimeout
}

func (s *RepoSet) evict(p *repoPair) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache[p.key] == p {
		delete(s.cache, p.key)
	}
}
