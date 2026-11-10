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
	"github.com/epheo/dotvirt/pkg/forge"
)

// The open-PR lane rides the inventory broadcast, but the forge is too slow for
// the broadcast hot path. So reads are pure cache hits, and a background
// refresher owns freshness: it re-queries the forge for a watched project when
// its repo's heads move (a propose/merge), when a nudge names its repo or finds
// its lane never read, and for all of them on a slow backstop tick - then wakes
// the hub only when some lane actually changed. The lane is project-scoped, not
// per-token: every member of a project sees the same PRs, and which are theirs
// is marked at read time.
const (
	// proposalsRefreshEvery is the backstop cadence; real freshness comes from the
	// git-change signal and explicit nudges.
	proposalsRefreshEvery = 60 * time.Second
	// proposalsTrackFor keeps a token watched after it last built an inventory
	// and closed its last stream. Frames rebuild only when something they show
	// moves, so a quiet open tab stays watched through its stream, not through
	// rebuilds; tokens gone longer than this stop being refreshed and their
	// cached lanes age out.
	proposalsTrackFor = 5 * time.Minute
	// proposalsCacheTTL bounds a cached lane's life without a refresh. It must
	// exceed proposalsRefreshEvery so watched tokens never miss between cycles.
	proposalsCacheTTL = 5 * time.Minute
)

// propTarget is one token's refresh target: whose lane, over which projects.
// readsPlatform is the last build's platform read verdict, the one a build
// whose review failed carries: a platform entry a propose tracked
// (trackProposalsProject) is no verdict.
type propTarget struct {
	id            auth.Identity
	projects      []project.ProjectInfo
	readsPlatform bool
	lastSeen      time.Time
}

// proposalsFor returns the open PRs across id's projects, marked with the ones
// id opened - a pure cache read on the broadcast hot path. It registers id as a
// refresh target; a project not cached yet nudges the refresher and is left out
// of this frame (the refresher wakes the hub when its lane lands). Nil when no
// project has a lane yet, so a cold frame ships without one. keepPlatform
// carries a platform reader's lane through a build whose platform review
// failed. A caller who does not read the platform tier, or sees part of a
// project, gets their own draft's row there alone, off the lane the refresher
// reads whenever anyone looks: no forge call, and no tracked entry for a
// rebuild to drop. A caller who sees every namespace but lacks a kind keeps
// the rows: only opening a PR weighs the kinds.
func (s *Server) proposalsFor(id auth.Identity, projects []project.ProjectInfo, keepPlatform bool) []model.Proposal {
	out := []model.Proposal{}
	hit, cold := false, false
	tracked, readsPlatform := s.trackProposals(id, projects, keepPlatform)
	partial := s.partialProjects(tracked)
	for _, p := range tracked {
		rows, ok := s.proposals.Get(p.Name)
		if !ok {
			cold = true
			continue
		}
		hit = true
		if partial[p.Name] {
			out = append(out, s.ownRows(id, p, rows)...)
			continue
		}
		for _, row := range rows {
			row.Mine = s.reader.OwnsProposal(id, p, row.Branch)
			out = append(out, row)
		}
	}
	if !readsPlatform && s.cfg.PlatformRepo != "" {
		if rows, ok := s.proposals.Get(platformProjectName); ok {
			hit = true
			out = append(out, s.ownRows(id, s.platformProject(), rows)...)
		} else {
			cold = true
		}
	}
	if cold {
		s.nudgeProposals("")
	}
	if !hit && cold {
		return nil
	}
	return out
}

// ownRows is the rows of proj's lane on id's draft branch, marked theirs.
// Their revert branches are left out: opening a revert takes reading the repo,
// and its diff restores others' files.
func (s *Server) ownRows(id auth.Identity, proj project.ProjectInfo, rows []model.Proposal) []model.Proposal {
	var out []model.Proposal
	branch := s.reader.ProposedBranch(id, proj)
	for _, row := range rows {
		if row.Branch == branch {
			row.Mine = true
			out = append(out, row)
		}
	}
	return out
}

// ownRow is id's own open PR numbered n in proj, off the cached lane: another's
// PR, a closed one and none at all answer alike, before anything reads the
// forge or the repo.
func (s *Server) ownRow(id auth.Identity, proj project.ProjectInfo, n int) (model.Proposal, bool) {
	rows, _ := s.proposals.Get(proj.Name)
	for _, row := range s.ownRows(id, proj, rows) {
		if row.PRNumber == n {
			return row, true
		}
	}
	return model.Proposal{}, false
}

// trackProposals records id as a live refresh target and returns the projects
// it watches, and whether the platform tier is among them by id's platform
// read. Called on every inventory build, so the watched set mirrors who is
// actually looking. Builds include the synthetic platform tier for its
// readers (InventoryForIdentity seeds it). keepPlatform says the platform
// review failed this build: the last build's reader verdict then carries
// forward so the lane survives the blip. Otherwise its absence is a verdict,
// and a demoted reader stops seeing others' platform PRs.
func (s *Server) trackProposals(id auth.Identity, projects []project.ProjectInfo, keepPlatform bool) ([]project.ProjectInfo, bool) {
	key := restfactory.TokenKey(id.Token)
	s.propMu.Lock()
	defer s.propMu.Unlock()
	reads := hasProject(projects, platformProjectName)
	if prev, ok := s.propTargets[key]; ok && keepPlatform && !reads && prev.readsPlatform {
		if p, found := findProject(prev.projects, platformProjectName); found {
			projects, reads = append(projects, p), true
		}
	}
	s.propTargets[key] = propTarget{id: id, projects: projects, readsPlatform: reads, lastSeen: time.Now()}
	return projects, reads
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

// nudgeProposals asks the refresher for an out-of-cycle pass (coalesced). A
// non-empty repo (canonical URL) marks that repo's projects due even though no
// head moved; with none, the pass picks up only lanes never read.
func (s *Server) nudgeProposals(repo string) {
	if repo != "" {
		s.propMu.Lock()
		s.propDirty[repo] = struct{}{}
		s.propMu.Unlock()
	}
	select {
	case s.propNudge <- struct{}{}:
	default:
	}
}

// proposalOpened is what a handler does once it has pushed and opened a PR:
// track the project for the caller, nudge its repo, and poke the repo so the
// fetch publishes GitChanged for the pushed branch (the inventory and declared
// files follow it). The nudge covers a fetch that saw no head move: a poll that
// landed between the push and the PR, or a failed fetch. The order matters - a
// pass only queries tracked projects (see trackProposalsProject).
func (s *Server) proposalOpened(sc scope) {
	s.trackProposalsProject(sc.id, sc.proj)
	s.nudgeProposals(forge.NormalizeRepoURL(sc.proj.Repo))
	s.repos.Poke(sc.proj.Repo)
}

// RunProposalsRefresher drives the lane's freshness off the hot path: it blocks on
// {GitChanged from the bus, a handler nudge, the backstop tick}, re-queries the
// forge for the watched projects that are due, and publishes ProposalsChanged when
// a lane differs from the cache. It subscribes to GitChanged ONLY (not the
// cluster/live kinds), so a VM phase change never triggers a forge re-query, and
// a push re-queries only the projects of the repo it moved.
func (s *Server) RunProposalsRefresher(ctx context.Context, bus *eventbus.Bus) {
	gitChanged, cancel := bus.Subscribe(eventbus.GitChanged)
	defer cancel()
	t := time.NewTicker(proposalsRefreshEvery)
	defer t.Stop()
	merged := mergedReads{}
	for {
		all := false
		select {
		case <-ctx.Done():
			return
		case <-gitChanged:
		case <-s.propNudge:
		case <-t.C:
			all = true
		}
		if s.refreshProposals(all) {
			bus.Publish(eventbus.ProposalsChanged)
		}
		s.refreshMerged(merged, all)
	}
}

// refreshProposals re-queries the forge for the watched projects that are due,
// updates the cache, and reports whether any open lane changed. A project is due when all is set (the backstop), its lane was never
// read, a nudge named its repo, or its repo's GitChanged version moved since its
// lane was read. Expired targets are dropped. A failed lookup counts as a read and
// keeps the last rows: frame builds keep nudging for a cold lane, and each nudge
// must not reach a failing forge, so only the backstop, a head move or a nudge
// naming its repo retries it. One round-trip per project, however many tokens or
// users watch it.
func (s *Server) refreshProposals(all bool) bool {
	now := time.Now()
	s.propMu.Lock()
	watched := map[string]project.ProjectInfo{}
	for key, t := range s.propTargets {
		if !s.streamOpen(key) && now.Sub(t.lastSeen) > proposalsTrackFor {
			delete(s.propTargets, key)
			continue
		}
		for _, p := range t.projects {
			watched[p.Name] = p
		}
	}
	// The platform tier's lane is read whenever anyone looks, a caller who
	// sees no tenant project included: frames ask the platform-reader review
	// only once it holds a proposal (platformBusy).
	if len(s.propTargets) > 0 && s.cfg.PlatformRepo != "" {
		watched[platformProjectName] = s.platformProject()
	}
	dirty := s.propDirty
	s.propDirty = map[string]struct{}{}
	seen := map[string]uint64{}
	for name := range watched {
		if v, ok := s.propSeen[name]; ok {
			seen[name] = v
		}
	}
	s.propMu.Unlock()

	anyChanged := false
	for name, p := range watched {
		repo := forge.NormalizeRepoURL(p.Repo)
		// Read before the query: a push landing mid-query leaves the project due.
		ver := s.bus.KeyVersion(eventbus.GitChanged, repo)
		prev, cached := s.proposals.Get(name)
		last, read := seen[name]
		_, nudged := dirty[repo]
		if !all && read && last == ver && !nudged {
			continue
		}
		rows, err := s.reader.OpenProposals(p)
		seen[name] = ver
		if err != nil {
			log.Printf("proposals: %s: %v (skipping)", name, err)
			if cached {
				s.proposals.Put(name, prev)
			}
			continue
		}
		if rows == nil {
			rows = []model.Proposal{}
		}
		if cached {
			if !proposalsEqual(prev, rows) {
				anyChanged = true
			}
		} else if len(rows) > 0 {
			// A cold lane that stays empty isn't a visible change - don't wake the hub.
			anyChanged = true
		}
		s.proposals.Put(name, rows)
	}

	s.propMu.Lock()
	s.propSeen = seen
	s.propMu.Unlock()
	return anyChanged
}

// mergedReads is, per watched repo, the GitChanged version its merged lane was
// last read at and whether a backstop tick has read it again since. Owned by
// the refresher goroutine.
type mergedReads map[string]mergedRead

type mergedRead struct {
	ver     uint64
	settled bool
}

// refreshMerged re-derives the merged-PR lane of each watched repo whose head
// moved since its last read: the poll backstop behind the webhook's instant
// record, and what reseeds the lane after a restart. A merge moves its base
// head, so a quiet repo costs no forge call. The forge pushes a merge before it
// marks the PR merged, so a read on the move alone can miss it: the next
// backstop tick (settle) reads once more, and a failed read is retried on each
// tick until one lands. Repos are deduped (the lane is project-scoped, not
// per-token; visibility applies at read time). The feed publishes only on
// actual change, so the steady-state pass is silent.
func (s *Server) refreshMerged(reads mergedReads, settle bool) {
	if s.tasks == nil {
		return
	}
	byRepo := map[string]project.ProjectInfo{}
	s.propMu.Lock()
	for _, t := range s.propTargets {
		for _, p := range t.projects {
			if p.Repo != "" {
				byRepo[forge.NormalizeRepoURL(p.Repo)] = p
			}
		}
	}
	anyone := len(s.propTargets) > 0
	s.propMu.Unlock()
	if anyone && s.cfg.PlatformRepo != "" {
		byRepo[forge.NormalizeRepoURL(s.cfg.PlatformRepo)] = s.platformProject()
	}
	for repo := range reads {
		if _, ok := byRepo[repo]; !ok {
			delete(reads, repo)
		}
	}
	since := time.Now().Add(-tasks.MergeRetention)
	for repo, p := range byRepo {
		// Read before the query: a merge landing mid-query leaves the repo due.
		ver := s.bus.KeyVersion(eventbus.GitChanged, repo)
		last, read := reads[repo]
		again := read && last.ver == ver
		if again && (last.settled || !settle) {
			continue
		}
		merges, err := s.reader.RecentlyMerged(p, since)
		reads[repo] = mergedRead{ver: ver, settled: again && err == nil}
		if err != nil {
			log.Printf("tasks: merged PRs for %s: %v (skipping)", p.Name, err)
			continue
		}
		for _, m := range merges {
			s.tasks.RecordMerge(m)
		}
	}
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
