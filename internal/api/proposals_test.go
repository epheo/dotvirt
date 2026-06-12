package api

import (
	"sync"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// fakeDraft implements only OpenProposal; the embedded interface panics on
// anything else, which is exactly what these tests want.
type fakeDraft struct {
	Draft
	mu    sync.Mutex
	prs   map[string]model.Proposal // project name → its open PR
	calls int
}

func (f *fakeDraft) OpenProposal(id auth.Identity, proj project.ProjectInfo) (model.Proposal, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	pr, ok := f.prs[proj.Name]
	return pr, ok, nil
}

func (f *fakeDraft) forgeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestProposalsHotPathNeverCallsForge pins the refresher contract: proposalsFor
// (the broadcast hot path) is a pure cache read — all forge traffic happens in
// refreshProposals, and the hub is woken only when a lane visibly changes.
func TestProposalsHotPathNeverCallsForge(t *testing.T) {
	fd := &fakeDraft{prs: map[string]model.Proposal{}}
	s := NewServer(Deps{Draft: fd})
	id := auth.Identity{Token: "tok-alice", Username: "alice"}
	projects := []project.ProjectInfo{{Name: "team-a", Repo: "http://x/r.git"}}

	if got := s.proposalsFor(id, projects); got != nil {
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
	if s.refreshProposals() {
		t.Fatal("empty cold lane should not wake the hub")
	}
	if got := s.proposalsFor(id, projects); got == nil || len(got) != 0 {
		t.Fatalf("after refresh the lane should be cached empty, got %v", got)
	}

	// A PR opens: the next pass must report a change and the lane must carry it.
	fd.mu.Lock()
	fd.prs["team-a"] = model.Proposal{Project: "team-a", PRNumber: 7, PRURL: "http://x/pr/7"}
	fd.mu.Unlock()
	if !s.refreshProposals() {
		t.Fatal("a new PR should wake the hub")
	}
	got := s.proposalsFor(id, projects)
	if len(got) != 1 || got[0].PRNumber != 7 {
		t.Fatalf("lane = %v, want PR #7", got)
	}

	// Same state again: no change, no wake.
	if s.refreshProposals() {
		t.Fatal("an unchanged lane should not wake the hub")
	}

	// The PR merges (disappears): change again.
	fd.mu.Lock()
	delete(fd.prs, "team-a")
	fd.mu.Unlock()
	if !s.refreshProposals() {
		t.Fatal("a merged PR should wake the hub")
	}
	if got := s.proposalsFor(id, projects); len(got) != 0 {
		t.Fatalf("lane should be empty after the merge, got %v", got)
	}
}

// TestProposalsTargetExpiry verifies a token that stops building inventories
// drops out of the refresh set, so dead sessions cost no forge traffic.
func TestProposalsTargetExpiry(t *testing.T) {
	fd := &fakeDraft{prs: map[string]model.Proposal{}}
	s := NewServer(Deps{Draft: fd})
	id := auth.Identity{Token: "tok-bob", Username: "bob"}
	s.proposalsFor(id, []project.ProjectInfo{{Name: "team-b"}})

	// Age the target past the tracking window.
	s.propMu.Lock()
	for key, tgt := range s.propTargets {
		tgt.lastSeen = time.Now().Add(-proposalsTrackFor - time.Minute)
		s.propTargets[key] = tgt
	}
	s.propMu.Unlock()

	s.refreshProposals()
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
