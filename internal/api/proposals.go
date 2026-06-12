package api

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// The open-PR lane rides the inventory broadcast, but the forge is too slow for
// the broadcast hot path. So reads are pure cache hits, and a background
// refresher owns freshness: it re-queries the forge per watched token when git
// heads move (a propose/merge), when a handler nudges it, and on a slow backstop
// tick — then wakes the hub only when some lane actually changed.
const (
	// proposalsRefreshEvery is the backstop cadence; real freshness comes from the
	// git-change signal and explicit nudges.
	proposalsRefreshEvery = 60 * time.Second
	// proposalsTrackFor keeps a token watched after it last built an inventory.
	// Active subscribers re-track every heartbeat (15s); tokens gone longer than
	// this stop being refreshed and their cached lanes age out.
	proposalsTrackFor = 5 * time.Minute
	// proposalsCacheTTL bounds a cached lane's life without a refresh. It must
	// exceed proposalsRefreshEvery so watched tokens never miss between cycles.
	proposalsCacheTTL = 5 * time.Minute
)

// propTarget is one token's refresh target: whose lane, over which projects.
type propTarget struct {
	id       auth.Identity
	projects []project.ProjectInfo
	lastSeen time.Time
}

// proposalsFor returns id's open PRs across its projects — a pure cache read on
// the broadcast hot path. It registers id as a refresh target; on a cold cache it
// nudges the refresher and ships this frame without the lane (the refresher wakes
// the hub when the lane lands).
func (s *Server) proposalsFor(id auth.Identity, projects []project.ProjectInfo) []model.Proposal {
	if s.draft == nil {
		return nil
	}
	s.trackProposals(id, projects)
	if v, ok := s.proposals.Get(restfactory.TokenKey(id.Token)); ok {
		return v
	}
	s.nudgeProposals()
	return nil
}

// trackProposals records id as a live refresh target. Called on every inventory
// build, so the watched set mirrors who is actually looking.
func (s *Server) trackProposals(id auth.Identity, projects []project.ProjectInfo) {
	s.propMu.Lock()
	defer s.propMu.Unlock()
	s.propTargets[restfactory.TokenKey(id.Token)] = propTarget{id: id, projects: projects, lastSeen: time.Now()}
}

// nudgeProposals asks the refresher for an out-of-cycle pass (coalesced). Handlers
// call it after a propose/revert so every subscriber's lane repaints without
// waiting for the git poll to notice the pushed branch.
func (s *Server) nudgeProposals() {
	select {
	case s.propNudge <- struct{}{}:
	default:
	}
}

// RunProposalsRefresher drives the lane's freshness off the hot path: it blocks on
// {git heads moved, a nudge, the backstop tick}, re-queries the forge for every
// watched token, and signals changed (the hub's bus) when a lane differs from the
// cache — so subscribers repaint within a debounce, not a heartbeat.
func (s *Server) RunProposalsRefresher(ctx context.Context, gitChanged <-chan struct{}, changed chan<- struct{}) {
	if s.draft == nil {
		return
	}
	t := time.NewTicker(proposalsRefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-gitChanged:
		case <-s.propNudge:
		case <-t.C:
		}
		if s.refreshProposals() {
			select {
			case changed <- struct{}{}:
			default:
			}
		}
	}
}

// refreshProposals re-queries the forge for every live target, updates the cache,
// and reports whether any lane changed. Expired targets are dropped. Best-effort
// per project: a failing forge lookup skips that project rather than failing the
// pass.
func (s *Server) refreshProposals() bool {
	now := time.Now()
	s.propMu.Lock()
	targets := make(map[string]propTarget, len(s.propTargets))
	for key, t := range s.propTargets {
		if now.Sub(t.lastSeen) > proposalsTrackFor {
			delete(s.propTargets, key)
			continue
		}
		targets[key] = t
	}
	s.propMu.Unlock()

	anyChanged := false
	for key, t := range targets {
		out := []model.Proposal{}
		for _, p := range t.projects {
			pr, ok, err := s.draft.OpenProposal(t.id, p)
			if err != nil {
				log.Printf("proposals: %s: %v (skipping)", p.Name, err)
				continue
			}
			if ok {
				out = append(out, pr)
			}
		}
		if prev, ok := s.proposals.Get(key); ok {
			if !proposalsEqual(prev, out) {
				anyChanged = true
			}
		} else if len(out) > 0 {
			// A cold lane that stays empty isn't a visible change — don't wake the hub.
			anyChanged = true
		}
		s.proposals.Put(key, out)
	}
	return anyChanged
}

// handleProposals lists the caller's open PRs across their visible projects — the
// same set the live inventory now carries; kept as a standalone read for parity.
func (s *Server) handleProposals(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	projects, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := s.proposalsFor(id, projects)
	if out == nil {
		out = []model.Proposal{}
	}
	writeJSON(w, http.StatusOK, out)
}

func proposalsEqual(a, b []model.Proposal) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
