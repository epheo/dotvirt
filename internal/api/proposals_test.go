package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
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

	merged      []tasks.Merge // what the forge lists as merged
	mergedFail  bool
	mergedCalls int
	settled     []string // each SettlePushed, as "repo branch #n head merged"
}

func (f *fakeDraft) SettlePushed(repo, branch string, number int, head string, merged bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settled = append(f.settled, fmt.Sprintf("%s %s #%d %s %v", repo, branch, number, head, merged))
	return nil
}

func (f *fakeDraft) RecentlyMerged(project.ProjectInfo, time.Time) ([]tasks.Merge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mergedCalls++
	if f.mergedFail {
		return nil, errors.New("forge down")
	}
	return f.merged, nil
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

func (f *fakeDraft) ProposedBranch(id auth.Identity, _ project.ProjectInfo) string {
	return id.Username
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

// TestProposalsHeadMoveWakesHub pins the review caches' invalidation: a PR
// whose head moves under the same number is a lane change, so the clients
// keyed on the head re-read its review.
func TestProposalsHeadMoveWakesHub(t *testing.T) {
	fd := &fakeDraft{prs: map[string]model.Proposal{
		"team-a": {Project: "team-a", PRNumber: 7, Head: "aaa"},
	}}
	s := NewServer(Deps{Reader: fd, Draft: fd})
	id := auth.Identity{Token: "tok-alice", Username: "alice"}
	s.proposalsFor(id, []project.ProjectInfo{{Name: "team-a", Repo: "http://x/r.git"}}, false)
	if !s.refreshProposals(true) {
		t.Fatal("a PR should wake the hub")
	}
	fd.mu.Lock()
	fd.prs["team-a"] = model.Proposal{Project: "team-a", PRNumber: 7, Head: "bbb"}
	fd.mu.Unlock()
	if !s.refreshProposals(true) {
		t.Fatal("a moved head should wake the hub")
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

// TestCanReadPlatformNoRepo pins the safety gate: with no platform repo
// configured, the inventory never seeds (or SSAR-checks) the platform tier.
func TestCanReadPlatformNoRepo(t *testing.T) {
	s := NewServer(Deps{Reader: &fakeDraft{}, Draft: &fakeDraft{}}) // no PlatformRepo
	id := auth.Identity{Token: "tok", Username: "u"}
	// A nil cluster client is safe here: the repo-empty guard returns before any
	// SSAR, so it must not be dereferenced.
	if ok, err := s.canReadPlatform(context.TODO(), id, nil); ok || err != nil {
		t.Fatal("canReadPlatform must be false when no platform repo is configured")
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

// The merged lane costs a forge call only when its repo's head moved, plus one
// settling read on the next backstop tick: the forge pushes a merge before it
// marks the PR merged, so the read on the move can miss it.
func TestMergedLaneReadsOnHeadMoves(t *testing.T) {
	bus := eventbus.New()
	feed := tasks.New(bus)
	fd := &fakeDraft{prs: map[string]model.Proposal{}}
	s := NewServer(Deps{Reader: fd, Draft: fd, Bus: bus, Tasks: feed})
	repo := forge.NormalizeRepoURL("https://forge/o/a.git")
	s.trackProposals(auth.Identity{Token: "tok-alice", Username: "alice"},
		[]project.ProjectInfo{{Name: "team-a", Repo: "https://forge/o/a.git"}}, false)
	reads := mergedReads{}
	pass := func(step string, tick bool, want int) {
		t.Helper()
		fd.mu.Lock()
		fd.mergedCalls = 0
		fd.mu.Unlock()
		s.refreshMerged(reads, tick)
		fd.mu.Lock()
		defer fd.mu.Unlock()
		if fd.mergedCalls != want {
			t.Fatalf("%s: %d merged-lane reads, want %d", step, fd.mergedCalls, want)
		}
	}
	set := func(fn func()) {
		fd.mu.Lock()
		fn()
		fd.mu.Unlock()
	}

	pass("cold lane", false, 1)
	pass("nothing moved", false, 0)
	pass("settle the reseed", true, 1)
	pass("quiet backstop", true, 0)

	bus.PublishKey(eventbus.GitChanged, repo)
	pass("head moved, PR not marked merged yet", false, 1)
	set(func() { fd.merged = []tasks.Merge{{RepoURL: repo, Number: 7, At: time.Now()}} })
	pass("no move", false, 0)
	if len(feed.Merges()) != 0 {
		t.Fatal("merge recorded before the forge listed it")
	}
	pass("settle the move", true, 1)
	if m := feed.Merges(); len(m) != 1 || m[0].Number != 7 {
		t.Fatalf("settling read missed the merge: %+v", m)
	}
	pass("settled", true, 0)

	set(func() { fd.mergedFail = true })
	bus.PublishKey(eventbus.GitChanged, repo)
	pass("failing read on a move", false, 1)
	pass("failure waits for the backstop", false, 0)
	pass("failed settle", true, 1)
	set(func() { fd.mergedFail = false })
	pass("retried on the next tick", true, 1)
	pass("settled after recovery", true, 0)
}

// The platform lane is read whenever anyone looks, a caller who sees no
// tenant project included, so the first platform PR shows after a restart.
func TestPlatformLaneReadWithNoTenantWatched(t *testing.T) {
	fd := &fakeDraft{prs: map[string]model.Proposal{platformProjectName: {Project: platformProjectName, PRNumber: 1}}}
	s := NewServer(Deps{Reader: fd, Draft: fd, Config: Config{PlatformRepo: "https://forge/dotvirt/platform.git"}})
	s.proposalsFor(auth.Identity{Token: "tok", Username: "u"}, nil, false)
	if !s.refreshProposals(true) {
		t.Fatal("an open platform PR read with no tenant watched should wake the hub")
	}
	if !s.platformBusy() {
		t.Fatal("the frame should see the platform busy")
	}
}

// A non-reader's frame before the platform lane is read ships no lane and
// nudges the refresher, whose next pass reads it, not the backstop. Once
// read, their draft's row shows, beside a tenant lane still unread.
func TestNonReaderColdPlatformLaneNudges(t *testing.T) {
	fd := &fakeDraft{prs: map[string]model.Proposal{platformProjectName: {Project: platformProjectName, PRNumber: 1, Branch: "alice"}}}
	s := NewServer(Deps{Reader: fd, Draft: fd, Config: Config{PlatformRepo: "https://forge/dotvirt/platform.git"}})
	alice := auth.Identity{Token: "tok-alice", Username: "alice"}
	nudged := func() bool {
		select {
		case <-s.propNudge:
			return true
		default:
			return false
		}
	}
	if got := s.proposalsFor(alice, nil, false); got != nil {
		t.Fatalf("cold platform lane = %v, want no lane", got)
	}
	if !nudged() {
		t.Fatal("a cold platform lane must nudge the refresher")
	}
	s.refreshProposals(false)
	if got := s.proposalsFor(alice, nil, false); len(got) != 1 || got[0].PRNumber != 1 || !got[0].Mine {
		t.Fatalf("after the nudged pass = %+v, want her PR #1", got)
	}
	if nudged() {
		t.Error("a read platform lane nudged again")
	}
	teamA := []project.ProjectInfo{{Name: "team-a", Repo: "http://x/a.git"}}
	if got := s.proposalsFor(alice, teamA, false); len(got) != 1 || got[0].PRNumber != 1 {
		t.Errorf("with team-a unread = %+v, want her platform PR", got)
	}
	if !nudged() {
		t.Error("an unread tenant lane must nudge the refresher")
	}
}

// A proposal's review asks the forge as dotvirt, so the caller reads what
// failed and its class, never the forge's answer: no API path, host, body or
// token. The forge refusing dotvirt is a 503, never the caller's 403.
func TestProposalRouteNeverEchoesTheForge(t *testing.T) {
	const token = "forge-token-7f3a"
	var status atomic.Int32
	forgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"repo-internal-detail at `+r.URL.Path+` for `+r.Header.Get("Authorization")+`","url":"https://forge.internal.example/api/swagger"}`, int(status.Load()))
	}))
	t.Cleanup(forgeSrv.Close)
	fc := newFakeCluster(t)
	fc.namespaces[0].Annotations["dotvirt.io/repo"] = "file://" + seedPlatformRepo(t, map[string]string{"README.md": "seed\n"})
	fc.readsVMs = func(_, ns string) bool { return ns == "team-a-dev" }
	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	c := changeset.New(store, repos, forge.NewFactory(forgeSrv.URL, forge.StaticToken(token), false, ""), nil, nil, nil, "main", "dotvirt/proposed")
	s := fc.server(c, Config{BaseBranch: "main"})

	for _, tc := range []struct {
		forge, want int
		says        string
	}{
		{http.StatusNotFound, http.StatusNotFound, "the forge has no pull request #7"},
		{http.StatusForbidden, http.StatusServiceUnavailable, "the forge refused dotvirt (403)"},
		{http.StatusInternalServerError, http.StatusServiceUnavailable, "the forge failed (500)"},
	} {
		status.Store(int32(tc.forge))
		rec := do(s, "tenant", http.MethodGet, "/api/projects/team-a/proposals/7", "")
		body := rec.Body.String()
		if rec.Code != tc.want || !strings.Contains(body, tc.says) {
			t.Errorf("forge %d: route = %d %q, want %d saying %q", tc.forge, rec.Code, body, tc.want, tc.says)
		}
		for _, leak := range []string{strings.TrimPrefix(forgeSrv.URL, "http://"), "/api/v1", "repo-internal-detail", "forge.internal.example", token} {
			if strings.Contains(body, leak) {
				t.Errorf("forge %d: response %q carries %q", tc.forge, body, leak)
			}
		}
	}
}

// laneDraft is the gate rig's coordinator answering platform reviews: a
// reader's from the forge (#99 is one the forge lacks), an owner's from the
// lane row (foreign is the PR whose branch carries another's commit). Every
// review is recorded.
type laneDraft struct {
	*gatedDraft
	foreign int
	mu      sync.Mutex
	reviews []string
}

func (d *laneDraft) Proposal(proj project.ProjectInfo, n int) (model.ProposalDetail, error) {
	d.review(fmt.Sprintf("Proposal %d", n))
	if n == 99 {
		return model.ProposalDetail{}, fmt.Errorf("%w: the forge has no pull request #%d", model.ErrNotFound, n)
	}
	return model.ProposalDetail{Proposal: model.Proposal{Project: proj.Name, PRNumber: n, Branch: platformLane[n-1].Branch}, Items: []model.DraftItem{{Name: "theirs"}}}, nil
}

func (d *laneDraft) OwnProposal(_ auth.Identity, proj project.ProjectInfo, row model.Proposal) (model.ProposalDetail, error) {
	d.review(fmt.Sprintf("OwnProposal %d", row.PRNumber))
	if row.PRNumber == d.foreign {
		return model.ProposalDetail{}, fmt.Errorf("pull request #%d: %w", row.PRNumber, git.ErrForeignCommit)
	}
	return model.ProposalDetail{Proposal: model.Proposal{Project: proj.Name, PRNumber: row.PRNumber, Branch: row.Branch}, Items: []model.DraftItem{{Name: "mine"}}}, nil
}

func (d *laneDraft) review(call string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reviews = append(d.reviews, call)
}

func (d *laneDraft) takeReviews() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.reviews
	d.reviews = nil
	return out
}

// platformLane is the open platform PRs a lane rig caches: alice's draft, bob's,
// her legacy revert, her draft someone pushed to, and her revert.
var platformLane = []model.Proposal{
	{Project: platformProjectName, PRNumber: 1, Branch: "alice"},
	{Project: platformProjectName, PRNumber: 2, Branch: "bob"},
	{Project: platformProjectName, PRNumber: 3, Branch: "legacy-alice"},
	{Project: platformProjectName, PRNumber: 4, Branch: "alice"},
	{Project: platformProjectName, PRNumber: 5, Branch: "revert/alice"},
}

// platformReader is the grant reading the lane rig's platform repo, which
// declares nothing: listing namespaces cluster-wide.
var platformReader = []grant{{verb: verbList, resource: "namespaces"}}

// newLaneRig is a grant rig over laneDraft, its platform lane cached. Its
// callers see no project, so a frame reads no repo.
func newLaneRig(t *testing.T) (*gateRig, *laneDraft) {
	g := newGrantCluster(t)
	g.fc.readsVMs = func(string, string) bool { return false }
	d := &laneDraft{gatedDraft: &gatedDraft{planeDraft: &planeDraft{}}, foreign: 4}
	g.d = d.gatedDraft
	g.s = g.fc.server(d, Config{PlatformRepo: platformRepo})
	g.s.proposals.Put(platformProjectName, platformLane)
	return g, d
}

// failPlatformRead fails every review of listing namespaces, the platform
// read's first question; a token asked before keeps its cached verdict.
func (g *gateRig) failPlatformRead() {
	g.fc.mu.Lock()
	defer g.fc.mu.Unlock()
	g.fc.ssarFail = func(a *authzv1.ResourceAttributes) int {
		if a.Verb == verbList && a.Group == "" && a.Resource == "namespaces" {
			return http.StatusServiceUnavailable
		}
		return 0
	}
}

func doAs(s *Server, id auth.Identity, method, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r = r.WithContext(auth.NewContext(r.Context(), id))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

// A caller who may not read the platform repo sees, of its open PRs, exactly
// their own draft's, on every frame after their propose, a review that failed
// included; never a revert of theirs. A platform reader sees them all.
func TestFrameShowsANonReaderTheirOwnPlatformProposals(t *testing.T) {
	g, _ := newLaneRig(t)
	platformRows := func(id auth.Identity) (out []string) {
		t.Helper()
		inv, err := g.s.InventoryForIdentity(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range inv.Proposals {
			if p.Project == platformProjectName {
				out = append(out, fmt.Sprintf("#%d mine=%v", p.PRNumber, p.Mine))
			}
		}
		return out
	}
	want := func(who string, got []string, want ...string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s: platform rows %q, want %q", who, got, want)
		}
	}
	alice := auth.Identity{Token: g.token(nil), Username: "alice"}
	g.s.proposalOpened(scope{id: alice, proj: g.s.platformProject()})
	want("alice", platformRows(alice), "#1 mine=true", "#4 mine=true")
	want("alice rebuilt", platformRows(alice), "#1 mine=true", "#4 mine=true")
	want("bob", platformRows(auth.Identity{Token: g.token(nil), Username: "bob"}), "#2 mine=true")
	want("carol", platformRows(auth.Identity{Token: g.token(nil), Username: "carol"}))
	want("reader", platformRows(auth.Identity{Token: g.token(platformReader), Username: "alice"}),
		"#1 mine=true", "#2 mine=false", "#3 mine=true", "#4 mine=true", "#5 mine=true")

	// A propose tracks the platform for its caller, which is no read verdict:
	// a build whose review fails carries only their own rows.
	blip := auth.Identity{Token: g.token(nil), Username: "alice"}
	g.s.proposalOpened(scope{id: blip, proj: g.s.platformProject()})
	g.failPlatformRead()
	want("alice, review failed", platformRows(blip), "#1 mine=true", "#4 mine=true")
}

// The review of a platform PR opens to a non-reader for their own draft's
// alone, read off the lane and never the forge. Another's PR, a revert of
// theirs and one that does not exist answer the same refusal without a read;
// a review that failed answers the same for all. A branch someone else pushed
// to answers why, without its items. A reader reads any.
func TestPlatformReviewOpensToItsOwnerAlone(t *testing.T) {
	g, d := newLaneRig(t)
	alice := auth.Identity{Token: g.token(nil), Username: "alice"}
	get := func(id auth.Identity, n string) *httptest.ResponseRecorder {
		return doAs(g.s, id, http.MethodGet, "/api/projects/platform/proposals/"+n)
	}

	rec := get(alice, "1")
	wantStatus(t, rec, http.StatusOK)
	if detail := decodeBody[model.ProposalDetail](t, rec); !detail.Proposal.Mine || len(detail.Items) != 1 || detail.Items[0].Name != "mine" {
		t.Errorf("own review = %+v, want hers, marked mine", detail)
	}
	if got := d.takeReviews(); !slices.Equal(got, []string{"OwnProposal 1"}) {
		t.Errorf("own review read %q, want the lane row's review alone", got)
	}

	denied := get(alice, "2")
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), platformReadDenied) {
		t.Fatalf("another's PR = %d %q, want the platform repo's refusal", denied.Code, denied.Body.String())
	}
	for _, n := range []string{"3", "5", "7", "99"} {
		if rec := get(alice, n); rec.Code != denied.Code || rec.Body.String() != denied.Body.String() {
			t.Errorf("#%s = %d %q, want the same refusal as another's PR", n, rec.Code, rec.Body.String())
		}
	}
	if got := d.takeReviews(); len(got) != 0 {
		t.Errorf("refused reviews read %q, want nothing read", got)
	}

	rec = get(alice, "4")
	if body := rec.Body.String(); rec.Code != http.StatusForbidden ||
		!strings.Contains(body, "changed outside dotvirt since you proposed it; someone who can read the whole repo can review it") || strings.Contains(body, "items") {
		t.Errorf("own PR someone pushed to = %d %q, want the guided refusal without items", rec.Code, body)
	}

	reader := auth.Identity{Token: g.token(platformReader), Username: "carol"}
	rec = get(reader, "2")
	wantStatus(t, rec, http.StatusOK)
	if detail := decodeBody[model.ProposalDetail](t, rec); detail.Proposal.Mine || detail.Items[0].Name != "theirs" {
		t.Errorf("reader's review = %+v, want bob's from the forge", detail)
	}
	wantStatus(t, get(reader, "99"), http.StatusNotFound)
	d.takeReviews()

	blip := auth.Identity{Token: g.token(nil), Username: "alice"}
	g.failPlatformRead()
	failed := get(blip, "1")
	if missing := get(blip, "99"); failed.Code != http.StatusServiceUnavailable || missing.Code != failed.Code || missing.Body.String() != failed.Body.String() {
		t.Errorf("review failed: own = %d %q, missing = %d %q, want the same 503", failed.Code, failed.Body.String(), missing.Code, missing.Body.String())
	}
	if got := d.takeReviews(); len(got) != 0 {
		t.Errorf("a failed review read %q", got)
	}
}

// The refreshers are the backstop behind the webhook: each open row and each
// recent merge goes to SettlePushed, a row the mirror holds no head for yet
// does not.
func TestRefreshersSettleWhatTheyRead(t *testing.T) {
	bus := eventbus.New()
	fd := &fakeDraft{
		prs: map[string]model.Proposal{
			"team-a": {Project: "team-a", PRNumber: 9, Branch: "dotvirt/proposed/alice/team-a-1", Head: "h9"},
			"team-b": {Project: "team-b", PRNumber: 3, Branch: "dotvirt/proposed/bob/team-b-2"},
		},
		merged: []tasks.Merge{{RepoURL: "https://forge/o/a", Number: 8, Branch: "dotvirt/proposed/carol/team-a-3", Head: "h8", At: time.Now()}},
	}
	s := NewServer(Deps{Reader: fd, Draft: fd, Bus: bus, Tasks: tasks.New(bus)})
	s.trackProposals(auth.Identity{Token: "tok-alice", Username: "alice"}, []project.ProjectInfo{
		{Name: "team-a", Repo: "https://forge/o/a.git"},
		{Name: "team-b", Repo: "https://forge/o/b.git"},
	}, false)
	s.refreshProposals(true)
	s.refreshMerged(mergedReads{}, true)
	fd.mu.Lock()
	defer fd.mu.Unlock()
	// The fake lists its merges for every repo: what counts is which calls.
	got := slices.Compact(slices.Sorted(slices.Values(fd.settled)))
	want := []string{
		"https://forge/o/a dotvirt/proposed/alice/team-a-1 #9 h9 false",
		"https://forge/o/a dotvirt/proposed/carol/team-a-3 #8 h8 true",
	}
	if !slices.Equal(got, want) {
		t.Errorf("settled %q, want %q", got, want)
	}
}
