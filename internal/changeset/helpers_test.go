package changeset

import (
	"strings"
	"testing"
)

func TestRefSegmentLegal(t *testing.T) {
	// Every output must be a legal git ref component: no forbidden chars, no '..',
	// no trailing '.lock', not '@', no '@{'.
	for _, in := range []string{
		"system:serviceaccount:tenant-a:viewer-a",
		"kube:admin",
		"a/b", "a..b", "release.lock", "user@{x", "@", "::::", "name~1", "x^y", "a b c", "",
	} {
		seg := refSegment(in)
		if seg == "" {
			t.Errorf("refSegment(%q) is empty", in)
		}
		for _, bad := range []string{":", "~", "^", " ", "?", "*", "[", "\\", "/", "..", "@{"} {
			if strings.Contains(seg, bad) {
				t.Errorf("refSegment(%q)=%q contains forbidden %q", in, seg, bad)
			}
		}
		if strings.HasSuffix(seg, ".lock") {
			t.Errorf("refSegment(%q)=%q ends in .lock", in, seg)
		}
		if seg == "@" {
			t.Errorf("refSegment(%q) is a bare @", in)
		}
	}
}

// TestProposedBranchNoCollision is the isolation guard: distinct (user, project)
// identities must never share a working branch, even when their readable segments
// sanitize identically.
func TestProposedBranchNoCollision(t *testing.T) {
	c := &Coordinator{proposed: "dotvirt/proposed"}

	// Pairs whose refSegment-ed forms collide but whose raw identities differ.
	collidingUsers := [][2]string{
		{"a:b", "a/b"},
		{"system:serviceaccount:team-a:bot", "system:serviceaccount:team:a-bot"},
		{"::::", "????"},
	}
	for _, p := range collidingUsers {
		if refSegment(p[0]) != refSegment(p[1]) {
			t.Logf("note: %q and %q don't actually collide under refSegment (still must differ)", p[0], p[1])
		}
		b0 := c.proposedBranch(p[0], "proj")
		b1 := c.proposedBranch(p[1], "proj")
		if b0 == b1 {
			t.Errorf("distinct users %q and %q share branch %q", p[0], p[1], b0)
		}
	}

	// Same identity is stable (re-propose targets the same branch).
	if a, b := c.proposedBranch("alice", "proj"), c.proposedBranch("alice", "proj"); a != b {
		t.Errorf("proposedBranch not stable for same identity: %q vs %q", a, b)
	}

	// Different project → different branch.
	if a, b := c.proposedBranch("alice", "p1"), c.proposedBranch("alice", "p2"); a == b {
		t.Errorf("same user, different project share branch %q", a)
	}
}
