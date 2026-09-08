package api

import (
	"context"
	"log"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/restfactory"
	"github.com/epheo/dotvirt/internal/tasks"
)

// The open-PR lane rides the inventory broadcast, but the forge is too slow for
// the broadcast hot path. So reads are pure cache hits, and a background
// refresher owns freshness: it re-queries the forge per watched project when
// git heads move (a propose/merge), when a handler nudges it, and on a slow
// backstop tick - then wakes the hub only when some lane actually changed. The
// lane is project-scoped, not per-token: every member of a project sees the
// same PRs, and which are theirs is marked at read time.
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

// proposalsFor returns the open PRs across id's projects, marked with the ones
// id opened - a pure cache read on the broadcast hot path. It registers id as a
// refresh target; a project not cached yet nudges the refresher and is left out
// of this frame (the refresher wakes the hub when its lane lands). Nil when no
// project has a lane yet, so a cold frame ships without one.
func (s *Server) proposalsFor(id auth.Identity, projects []project.ProjectInfo) []model.Proposal {
	if s.draft == nil {
		return nil
	}
	out := []model.Proposal{}
	hit, cold := false, false
	for _, p := range s.trackProposals(id, projects) {
		rows, ok := s.proposals.Get(p.Name)
		if !ok {
			cold = true
			continue
		}
		hit = true
		for _, row := range rows {
			row.Mine = s.draft.OwnsProposal(id, p, row.Branch)
			out = append(out, row)
		}
	}
	if cold {
		s.nudgeProposals()
	}
	if !hit && cold {
		return nil
	}
	return out
}

// trackProposals records id as a live refresh target and returns the projects
// it watches. Called on every inventory build, so the watched set mirrors who
// is actually looking. Builds include the synthetic platform tier
// (InventoryForIdentity seeds it), but a build whose platform discovery
// transiently failed would drop it; carry a previously tracked platform entry
// forward so the lane survives the blip.
func (s *Server) trackProposals(id auth.Identity, projects []project.ProjectInfo) []project.ProjectInfo {
	key := restfactory.TokenKey(id.Token)
	s.propMu.Lock()
	defer s.propMu.Unlock()
	if prev, ok := s.propTargets[key]; ok && !hasProject(projects, platformProjectName) {
		if p, found := findProject(prev.projects, platformProjectName); found {
			projects = append(projects, p)
		}
	}
	s.propTargets[key] = propTarget{id: id, projects: projects, lastSeen: time.Now()}
	return projects
}

// trackProposalsProject ensures proj is in id's refresh target before a propose
// nudges the refresher: the nudged pass only queries already-tracked projects, and a
// token that hasn't built an inventory yet (or whose platform tier discovery never
// lists) wouldn't be - so without this the new PR would wait for a later build.
func (s *Server) trackProposalsProject(id auth.Identity, proj project.ProjectInfo) {
	key := restfactory.TokenKey(id.Token)
	s.propMu.Lock()
	defer s.propMu.Unlock()
	t, ok := s.propTargets[key]
	if !ok {
		t = propTarget{id: id}
	}
	if !hasProject(t.projects, proj.Name) {
		t.projects = append(t.projects, proj)
	}
	t.lastSeen = time.Now()
	s.propTargets[key] = t
}

func hasProject(ps []project.ProjectInfo, name string) bool {
	_, ok := findProject(ps, name)
	return ok
}

func findProject(ps []project.ProjectInfo, name string) (project.ProjectInfo, bool) {
	for _, p := range ps {
		if p.Name == name {
			return p, true
		}
	}
	return project.ProjectInfo{}, false
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
// {GitChanged from the bus, a handler nudge, the backstop tick}, re-queries the
// forge for every watched project, and publishes ProposalsChanged when a lane differs
// from the cache - so subscribers repaint within a debounce, not a heartbeat. It
// subscribes to GitChanged ONLY (not the cluster/live kinds), so a VM phase change
// never triggers a forge re-query.
func (s *Server) RunProposalsRefresher(ctx context.Context, bus *eventbus.Bus) {
	if s.draft == nil {
		return
	}
	gitChanged, cancel := bus.Subscribe(eventbus.GitChanged)
	defer cancel()
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
			bus.Publish(eventbus.ProposalsChanged)
		}
		s.refreshMerged()
	}
}

// refreshMerged re-derives the merged-PR lane from the forge for every project
// someone is watching - the poll backstop behind the webhook's instant record,
// and what reseeds the lane after a restart. Repos are deduped across watchers
// (the lane is project-scoped, not per-token; visibility applies at read time).
// The feed publishes only on actual change, so the steady-state pass is silent.
func (s *Server) refreshMerged() {
	if s.tasks == nil {
		return
	}
	s.propMu.Lock()
	byRepo := map[string]project.ProjectInfo{}
	for _, t := range s.propTargets {
		for _, p := range t.projects {
			if p.Repo != "" {
				byRepo[p.Repo] = p
			}
		}
	}
	s.propMu.Unlock()
	since := time.Now().Add(-tasks.MergeRetention)
	for _, p := range byRepo {
		merges, err := s.draft.RecentlyMerged(p, since)
		if err != nil {
			log.Printf("tasks: merged PRs for %s: %v (skipping)", p.Name, err)
			continue
		}
		for _, m := range merges {
			s.tasks.RecordMerge(m)
		}
	}
}

// refreshProposals re-queries the forge for every project some live target
// watches, updates the cache, and reports whether any lane changed. Expired
// targets are dropped. Best-effort per project: a failing forge lookup skips
// that project rather than failing the pass. One round-trip per project,
// however many tokens or users watch it.
func (s *Server) refreshProposals() bool {
	now := time.Now()
	s.propMu.Lock()
	watched := map[string]project.ProjectInfo{}
	for key, t := range s.propTargets {
		if now.Sub(t.lastSeen) > proposalsTrackFor {
			delete(s.propTargets, key)
			continue
		}
		for _, p := range t.projects {
			watched[p.Name] = p
		}
	}
	s.propMu.Unlock()

	anyChanged := false
	for name, p := range watched {
		rows, err := s.draft.OpenProposals(p)
		if err != nil {
			log.Printf("proposals: %s: %v (skipping)", name, err)
			continue
		}
		if rows == nil {
			rows = []model.Proposal{}
		}
		if prev, ok := s.proposals.Get(name); ok {
			if !proposalsEqual(prev, rows) {
				anyChanged = true
			}
		} else if len(rows) > 0 {
			// A cold lane that stays empty isn't a visible change - don't wake the hub.
			anyChanged = true
		}
		s.proposals.Put(name, rows)
	}
	return anyChanged
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
