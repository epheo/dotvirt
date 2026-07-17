package tasks

import (
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/eventbus"
)

func TestRecordOpRingAndPublish(t *testing.T) {
	bus := eventbus.New()
	f := New(bus)
	v0 := bus.Version(eventbus.TaskChanged)

	for i := 0; i < opsCap+10; i++ {
		f.RecordOp(Op{Verb: "Restart", Namespace: "ns", Name: "vm", By: "alice", OK: true, At: time.Now()})
	}
	ops := f.Ops()
	if len(ops) != opsCap {
		t.Fatalf("ring not capped: %d", len(ops))
	}
	if bus.Version(eventbus.TaskChanged) != v0+uint64(opsCap+10) {
		t.Fatalf("each op must publish")
	}
}

func TestRecordMergeDedupesAndPrunes(t *testing.T) {
	bus := eventbus.New()
	f := New(bus)
	now := time.Now()
	m := Merge{RepoURL: "https://forge/org/repo", Number: 7, Title: "add vm", By: "alice", At: now}

	f.RecordMerge(m)
	v := bus.Version(eventbus.TaskChanged)
	// The refresher re-recording the identical PR must not wake anyone.
	f.RecordMerge(m)
	if bus.Version(eventbus.TaskChanged) != v {
		t.Fatalf("identical re-record published")
	}
	if got := f.Merges(); len(got) != 1 || got[0].Number != 7 {
		t.Fatalf("merges = %+v", got)
	}

	// An entry beyond retention is pruned by the next write.
	f.RecordMerge(Merge{RepoURL: "https://forge/org/old", Number: 1, At: now.Add(-2 * MergeRetention)})
	if got := f.Merges(); len(got) != 1 || got[0].Number != 7 {
		t.Fatalf("stale merge survived prune: %+v", got)
	}
}

func TestMergeAuthor(t *testing.T) {
	cases := []struct {
		head, want string
	}{
		// dotvirt proposal branch: the first segment under the prefix is the user.
		{"dotvirt/proposed/alice/tenant-a-1a2b3c", "alice"},
		// Sanitized identity (refSegment is lossy but readable).
		{"dotvirt/proposed/system-admin/tenant-a-1a2b3c", "system-admin"},
		// Human PR from an unrelated branch: attribute the forge poster.
		{"feature/rework", "bob"},
		{"", "bob"},
	}
	for _, c := range cases {
		if got := MergeAuthor(c.head, "dotvirt/proposed", "bob"); got != c.want {
			t.Errorf("MergeAuthor(%q) = %q, want %q", c.head, got, c.want)
		}
	}
}
