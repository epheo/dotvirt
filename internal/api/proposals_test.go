package api

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// fakeDraft implements only the lane's methods; the embedded interface panics
// on anything else, which is exactly what these tests want.
type fakeDraft struct {
	Reader
	Draft
	mu    sync.Mutex
	prs   map[string]model.Proposal // project name -> its open PR
	fail  string                    // project whose lookup errors
	calls int
	by    map[string]int // forge calls per project
}

func (f *fakeDraft) OpenProposals(proj project.ProjectInfo) ([]model.Proposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.by == nil {
		f.by = map[string]int{}
	}
	f.by[proj.Name]++
	if proj.Name == f.fail {
		return nil, errors.New("forge down")
	}
	if pr, ok := f.prs[proj.Name]; ok {
		return []model.Proposal{pr}, nil
	}
	return nil, nil
}

// A row is the reader's when its branch names them - enough to pin the
// per-reader marking.
func (f *fakeDraft) OwnsProposal(id auth.Identity, _ project.ProjectInfo, branch string) bool {
	return branch == id.Username
}

func (f *fakeDraft) forgeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestProposalsHotPathNeverCallsForge pins the refresher contract: proposalsFor
// (the broadcast hot path) is a pure cache read - all forge traffic happens in
// refreshProposals, and the hub is woken only when a lane visibly changes.
func TestProposalsHotPathNeverCallsForge(t *testing.T) {
	fd := &fakeDraft{prs: map[string]model.Proposal{}}
	s := NewServer(Deps{Reader: fd, Draft: fd})
	id := auth.Identity{Token: "tok-alice", Username: "alice"}
	projects := []project.ProjectInfo{{Name: "team-a", Repo: "http://x/r.git"}}

	if got := s.proposalsFor(id, projects, false); got != nil {
		t.Fatalf("cold cache should yield no lane, got %v", got)
	}
	if fd.forgeCalls() != 0 {
		t.Fatalf("hot path hit the forge %d times; want 0", fd.forgeCalls())
	}
	select {
	case <-s.propNudge:
	default:
		t.Fatal("cold read should have nudged the refresher")
	}

	// First pass over an empty lane: cache fills, but nothing visible changed.
	if s.refreshProposals(true) {
		t.Fatal("empty cold lane should not wake the hub")
	}
	if got := s.proposalsFor(id, projects, false); got == nil || len(got) != 0 {
		t.Fatalf("after refresh the lane should be cached empty, got %v", got)
	}

	// A PR opens: the next pass must report a change and the lane must carry it.
	fd.mu.Lock()
	fd.prs["team-a"] = model.Proposal{Project: "team-a", PRNumber: 7, PRURL: "http://x/pr/7"}
	fd.mu.Unlock()
	if !s.refreshProposals(true) {
		t.Fatal("a new PR should wake the hub")
	}
	got := s.proposalsFor(id, projects, false)
	if len(got) != 1 || got[0].PRNumber != 7 {
		t.Fatalf("lane = %v, want PR #7", got)
	}

	// Same state again: no change, no wake.
	if s.refreshProposals(true) {
		t.Fatal("an unchanged lane should not wake the hub")
	}

	// The PR merges (disappears): change again.
	fd.mu.Lock()
	delete(fd.prs, "team-a")
	fd.mu.Unlock()
	if !s.refreshProposals(true) {
		t.Fatal("a merged PR should wake the hub")
	}
	if got := s.proposalsFor(id, projects, false); len(got) != 0 {
		t.Fatalf("lane should be empty after the merge, got %v", got)
	}
}

// TestProposalsLaneIsProjectScoped pins the shared lane: two users watching
// one project cost one forge round-trip per pass, both see the PR, and only
// its proposer sees it as theirs - the cached row itself stays unmarked.
func TestProposalsLaneIsProjectScoped(t *testing.T) {
	fd := &fakeDraft{prs: map[string]model.Proposal{
		"team-a": {Project: "team-a", PRNumber: 7, Branch: "alice"},
	}}
	s := NewServer(Deps{Reader: fd, Draft: fd})
	projects := []project.ProjectInfo{{Name: "team-a", Repo: "http://x/r.git"}}
	alice := auth.Identity{Token: "tok-alice", Username: "alice"}
	bob := auth.Identity{Token: "tok-bob", Username: "bob"}
	s.proposalsFor(alice, projects, false)
	s.proposalsFor(bob, projects, false)
	if !s.refreshProposals(true) {
		t.Fatal("a PR should wake the hub")
	}
	if fd.forgeCalls() != 1 {
		t.Fatalf("one project watched twice cost %d forge calls; want 1", fd.forgeCalls())
	}
	a, b := s.proposalsFor(alice, projects, false), s.proposalsFor(bob, projects, false)
	if len(a) != 1 || !a[0].Mine {
		t.Fatalf("alice's lane = %+v, want her PR marked mine", a)
	}
	if len(b) != 1 || b[0].Mine {
		t.Fatalf("bob's lane = %+v, want alice's PR unmarked", b)
	}
	if cached, _ := s.proposals.Get("team-a"); cached[0].Mine {
		t.Fatal("the cached row must stay unmarked")
	}
}

// TestProposalsPlatformProjectSticky pins the platform lane's lifecycle: the
// synthetic "platform" project is config-only and never in discovery, so a
// propose into it tracks it explicitly; a build whose platform review failed
// keeps it (else its PR shows briefly then vanishes); a build that decided
// the caller is no platform author drops it, so a demoted author stops seeing
// platform PRs.
func TestProposalsPlatformProjectSticky(t *testing.T) {
	fd := &fakeDraft{prs: map[string]model.Proposal{
		"platform": {Project: "platform", PRNumber: 1, PRURL: "http://x/pr/1"},
	}}
	s := NewServer(Deps{Reader: fd, Draft: fd})
	id := auth.Identity{Token: "tok-admin", Username: "kube:admin"}
	platform := project.ProjectInfo{Name: "platform", Repo: "http://x/platform.git"}
	teamA := []project.ProjectInfo{{Name: "team-a", Repo: "http://x/a.git"}}
	hasPlatform := func(rows []model.Proposal) bool {
		return len(rows) == 1 && rows[0].Project == "platform"
	}

	// A propose into the platform tier tracks it (handlePropose's pre-poke step).
	s.trackProposalsProject(id, platform)
	if !s.refreshProposals(true) {
		t.Fatal("platform PR should wake the hub after a propose tracks it")
	}
	if got := s.proposalsFor(id, []project.ProjectInfo{platform}, false); !hasPlatform(got) {
		t.Fatalf("platform lane = %v, want PR for platform", got)
	}

	if got := s.proposalsFor(id, teamA, true); !hasPlatform(got) {
		t.Fatalf("platform lane dropped by a build whose review failed: %v", got)
	}
	if got := s.proposalsFor(id, teamA, false); hasPlatform(got) {
		t.Fatalf("platform lane survived a build that found no platform authority: %v", got)
	}
}

// TestCanAuthorPlatformNoRepo pins the safety gate: with no platform repo
// configured, the inventory never seeds (or SSAR-checks) the platform tier.
func TestCanAuthorPlatformNoRepo(t *testing.T) {
	s := NewServer(Deps{Reader: &fakeDraft{}, Draft: &fakeDraft{}}) // no PlatformRepo
	id := auth.Identity{Token: "tok", Username: "u"}
	// A nil cluster client is safe here: the repo-empty guard returns before any
	// SSAR, so it must not be dereferenced.
	if ok, err := s.canAuthorPlatform(context.TODO(), id, nil); ok || err != nil {
		t.Fatal("canAuthorPlatform must be false when no platform repo is configured")
	}
}

// TestProposalsTargetExpiry verifies a token that stops building inventories
// drops out of the refresh set, so dead sessions cost no forge traffic.
func TestProposalsTargetExpiry(t *testing.T) {
	fd := &fakeDraft{prs: map[string]model.Proposal{}}
	s := NewServer(Deps{Reader: fd, Draft: fd})
	id := auth.Identity{Token: "tok-bob", Username: "bob"}
	s.proposalsFor(id, []project.ProjectInfo{{Name: "team-b"}}, false)

	// Age the target past the tracking window.
	s.propMu.Lock()
	for key, tgt := range s.propTargets {
		tgt.lastSeen = time.Now().Add(-proposalsTrackFor - time.Minute)
		s.propTargets[key] = tgt
	}
	s.propMu.Unlock()

	s.refreshProposals(true)
	if fd.forgeCalls() != 0 {
		t.Fatalf("expired target still hit the forge %d times", fd.forgeCalls())
	}
	s.propMu.Lock()
	left := len(s.propTargets)
	s.propMu.Unlock()
	if left != 0 {
		t.Fatalf("%d expired targets still tracked", left)
	}
}

// takeCalls returns the per-project forge calls since the last take.
func (f *fakeDraft) takeCalls() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	by := maps.Clone(f.by)
	f.by = nil
	if by == nil {
		by = map[string]int{}
	}
	return by
}

// TestProposalsRefreshOnlyDueProjects pins the per-repo fan-out: a push to one
// repo re-queries that repo's lane alone, a nudge naming a repo does the same,
// a failed lookup waits for the backstop, and only the backstop re-queries every
// lane.
func TestProposalsRefreshOnlyDueProjects(t *testing.T) {
	bus := eventbus.New()
	fd := &fakeDraft{prs: map[string]model.Proposal{}}
	s := NewServer(Deps{Reader: fd, Draft: fd, Bus: bus})
	id := auth.Identity{Token: "tok-alice", Username: "alice"}
	s.trackProposals(id, []project.ProjectInfo{
		{Name: "team-a", Repo: "https://forge/o/a.git"},
		{Name: "team-b", Repo: "https://forge/o/b"},
	}, false)
	want := func(step string, calls map[string]int) {
		t.Helper()
		if got := fd.takeCalls(); !maps.Equal(got, calls) {
			t.Fatalf("%s: forge calls %v, want %v", step, got, calls)
		}
	}

	s.refreshProposals(false)
	want("cold lanes", map[string]int{"team-a": 1, "team-b": 1})
	s.refreshProposals(false)
	want("nothing moved", map[string]int{})

	bus.PublishKey(eventbus.GitChanged, forge.NormalizeRepoURL("https://forge/o/a"))
	s.refreshProposals(false)
	want("push to a", map[string]int{"team-a": 1})

	s.nudgeProposals(forge.NormalizeRepoURL("https://forge/o/b.git"))
	s.refreshProposals(false)
	want("nudge for b", map[string]int{"team-b": 1})

	fd.mu.Lock()
	fd.fail = "team-a"
	fd.mu.Unlock()
	bus.PublishKey(eventbus.GitChanged, forge.NormalizeRepoURL("https://forge/o/a"))
	s.refreshProposals(false)
	want("failing push to a", map[string]int{"team-a": 1})
	s.refreshProposals(false)
	want("failed lane waits", map[string]int{})
	fd.mu.Lock()
	fd.fail = ""
	fd.mu.Unlock()

	s.refreshProposals(true)
	want("backstop", map[string]int{"team-a": 1, "team-b": 1})
}

// A failing forge must cost one query per backstop, not one per frame build:
// every build of a frame whose lane is cold nudges the refresher, and a lane
// that never caches turned each nudge into another forge round-trip.
func TestProposalsFailingForgeNotAmplified(t *testing.T) {
	fd := &fakeDraft{prs: map[string]model.Proposal{
		"team-a": {Project: "team-a", PRNumber: 7, PRURL: "http://x/pr/7"},
	}, fail: "team-b"}
	s := NewServer(Deps{Reader: fd, Draft: fd})
	id := auth.Identity{Token: "tok-alice", Username: "alice"}
	projects := []project.ProjectInfo{{Name: "team-a", Repo: "http://x/a.git"}, {Name: "team-b", Repo: "http://x/b.git"}}

	for range 5 {
		s.proposalsFor(id, projects, false)
		s.refreshProposals(false)
	}
	if got := fd.takeCalls(); got["team-b"] != 1 {
		t.Fatalf("a cold lane on a failing forge was queried %d times over 5 frame builds; want 1", got["team-b"])
	}

	fd.mu.Lock()
	fd.fail = "team-a"
	fd.mu.Unlock()
	s.refreshProposals(true)
	if got := s.proposalsFor(id, projects, false); len(got) != 1 || got[0].PRNumber != 7 {
		t.Fatalf("a failed refresh dropped the last known rows: %v", got)
	}
}

// Frames rebuild only when something they show moves, so a tab open on a quiet
// cluster builds once; its open stream, not rebuilds, must keep its lanes
// refreshed past the tracking window, and closing it must let them expire.
func TestProposalsOpenStreamKeepsTokenWatched(t *testing.T) {
	fd := &fakeDraft{prs: map[string]model.Proposal{}}
	s := NewServer(Deps{Reader: fd, Draft: fd})
	id := auth.Identity{Token: "tok-bob", Username: "bob"}

	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	stream := s.streaming(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	})
	req := httptest.NewRequest(http.MethodGet, "/api/inventory/stream", nil)
	req = req.WithContext(auth.NewContext(req.Context(), id))
	go func() {
		stream(httptest.NewRecorder(), req)
		close(done)
	}()
	<-entered

	s.proposalsFor(id, []project.ProjectInfo{{Name: "team-b"}}, false)
	age := func() {
		s.propMu.Lock()
		for key, tgt := range s.propTargets {
			tgt.lastSeen = time.Now().Add(-proposalsTrackFor - time.Minute)
			s.propTargets[key] = tgt
		}
		s.propMu.Unlock()
	}
	age()
	s.refreshProposals(true)
	if got := fd.takeCalls(); got["team-b"] != 1 {
		t.Fatalf("a token with an open stream stopped being refreshed: %v", got)
	}

	close(release)
	<-done
	age()
	s.refreshProposals(true)
	if got := fd.takeCalls(); len(got) != 0 {
		t.Fatalf("a closed stream's token is still refreshed: %v", got)
	}
}
