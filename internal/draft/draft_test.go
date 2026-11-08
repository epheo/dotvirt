package draft

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/epheo/dotvirt/internal/manifest"
)

func editEntry(ns, name string) Entry {
	mem := "2Gi"
	return Entry{Kind: KindEdit, Namespace: ns, Name: name, SourceFile: ns + "/" + name + ".yaml", Edit: &manifest.VMEdit{Memory: &mem}}
}

func TestStoreIsolatesByUserAndProject(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Same VM name, different (user, project) tuples - must not collide.
	if err := s.Stage("alice", "team-a", editEntry("tenant-a", "web")); err != nil {
		t.Fatal(err)
	}
	if err := s.Stage("alice", "team-b", editEntry("tenant-b", "web")); err != nil {
		t.Fatal(err)
	}
	if err := s.Stage("bob", "team-a", editEntry("tenant-a", "web")); err != nil {
		t.Fatal(err)
	}

	check := func(user, project string, want int) {
		got, err := s.List(user, project)
		if err != nil {
			t.Fatalf("List(%s,%s): %v", user, project, err)
		}
		if len(got) != want {
			t.Errorf("List(%s,%s) = %d entries, want %d", user, project, len(got), want)
		}
	}
	check("alice", "team-a", 1)
	check("alice", "team-b", 1)
	check("bob", "team-a", 1)
	check("alice", "nonexistent", 0)

	// Files land at <dir>/<user>/<project>.json.
	if _, err := os.Stat(filepath.Join(dir, "alice", "team-a.json")); err != nil {
		t.Errorf("expected draft file at alice/team-a.json: %v", err)
	}

	// Clearing one pair leaves the others intact.
	if err := s.Clear("alice", "team-a"); err != nil {
		t.Fatal(err)
	}
	check("alice", "team-a", 0)
	check("alice", "team-b", 1)
	check("bob", "team-a", 1)
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if err := s.Stage("alice", "team-a", editEntry("tenant-a", "web")); err != nil {
		t.Fatal(err)
	}

	// A fresh Store over the same dir loads the persisted draft from disk.
	s2, _ := Open(dir)
	got, err := s2.List("alice", "team-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "web" {
		t.Errorf("reopened store lost the draft: %+v", got)
	}
}

// Projects lists a user's non-empty drafts from disk, as a restarted backend
// finds them: neither another user's, an emptied one's, nor one holding only
// a stale create.
func TestProjectsListsHeldDrafts(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	for _, p := range []string{"team-a", "team/b", "emptied"} {
		if err := s.Stage("system:admin", p, editEntry("ns", "vm")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Stage("bob", "team-c", editEntry("ns", "vm")); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear("system:admin", "emptied"); err != nil {
		t.Fatal(err)
	}
	stale := `[{"kind":"create","namespace":"ns","name":"db"}]`
	if err := os.WriteFile(s.path("system:admin", "stale"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	reopened, _ := Open(dir)
	got, err := reopened.Projects("system:admin")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"team-a", "team/b"}) {
		t.Errorf("Projects = %q, want team-a and team/b", got)
	}
	if got, err := reopened.Projects("nobody"); err != nil || len(got) != 0 {
		t.Errorf("a user without drafts = %q, %v; want none", got, err)
	}
}

// TestStoreSafeSegments verifies identities with '/' or ':' map to one safe path
// segment (k8s usernames like "system:admin" must not create nested dirs).
func TestStoreSafeSegments(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if err := s.Stage("system:admin", "team/with/slashes", editEntry("ns", "vm")); err != nil {
		t.Fatal(err)
	}
	got, err := s.List("system:admin", "team/with/slashes")
	if err != nil || len(got) != 1 {
		t.Fatalf("round-trip through escaped segments failed: %+v err=%v", got, err)
	}
}

// TestClearRemovesFile checks Clear (and an Unstage that empties) deletes the
// on-disk file rather than leaving an empty [] behind.
func TestClearRemovesFile(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if err := s.Stage("alice", "team-a", editEntry("ns", "vm")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "alice", "team-a.json")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("draft file should exist after stage: %v", err)
	}
	if err := s.Clear("alice", "team-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("Clear should remove the draft file, stat err = %v", err)
	}

	// Unstaging the last entry should likewise remove the file.
	if err := s.Stage("bob", "team-b", editEntry("ns", "vm")); err != nil {
		t.Fatal(err)
	}
	if err := s.Unstage("bob", "team-b", ResourceVM, "ns", "vm"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bob", "team-b.json")); !os.IsNotExist(err) {
		t.Error("emptying a draft via Unstage should remove its file")
	}
}

// TestSafeSegmentNoTraversal verifies a '..' user/project can't escape the dir.
func TestSafeSegmentNoTraversal(t *testing.T) {
	for _, bad := range []string{"..", "."} {
		seg := safeSegment(bad)
		if seg == bad {
			t.Errorf("safeSegment(%q) = %q — must not pass a traversal segment through", bad, seg)
		}
	}
	// A normal dotted name is preserved (round-trips via PathEscape/Unescape).
	if got := safeSegment("v1.2"); got != "v1.2" {
		t.Errorf("safeSegment(\"v1.2\") = %q, want unchanged", got)
	}
}

// A create staged before manifests were rendered at stage time carries a spec
// and no manifest: nothing propose could commit. It is dropped on load while the
// rest of the draft survives, and the file is not treated as corrupt.
func TestStaleCreateDroppedOnLoad(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	p := s.path("alice", "team-a")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `[{"kind":"create","namespace":"tenant-a","name":"db","spec":{"name":"db","namespace":"tenant-a"}},
		{"kind":"edit","namespace":"tenant-a","name":"web","sourceFile":"tenant-a/web.yaml","edit":{"memory":"2Gi"}}]`
	if err := os.WriteFile(p, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List("alice", "team-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "web" {
		t.Fatalf("want only the edit to survive, got %+v", entries)
	}
	if _, err := os.Stat(p + ".corrupt"); !os.IsNotExist(err) {
		t.Error("a stale create is not corruption; the file must not be quarantined")
	}
}

// A corrupt draft file must not wedge its (user, project) forever: it is
// quarantined (kept as .corrupt for inspection) and the draft starts fresh.
func TestCorruptDraftQuarantined(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := s.path("alice", "team-a")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := s.List("alice", "team-a")
	if err != nil {
		t.Fatalf("List over corrupt draft: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want fresh draft, got %+v", entries)
	}
	if _, err := os.Stat(p + ".corrupt"); err != nil {
		t.Fatalf("corrupt file not quarantined: %v", err)
	}

	// Staging works again and persists over the quarantined file's slot.
	if err := s.Stage("alice", "team-a", editEntry("tenant-a", "web")); err != nil {
		t.Fatalf("Stage after quarantine: %v", err)
	}
	if entries, err = s.List("alice", "team-a"); err != nil || len(entries) != 1 {
		t.Fatalf("List after re-stage: %v, %+v", err, entries)
	}
}

// Propose unstages what it committed; an entry restaged meanwhile is newer
// work and must survive, on disk too.
func TestRemoveKeepsRestagedEntries(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if err := s.Stage("alice", "p", editEntry("ns", "a"), editEntry("ns", "b")); err != nil {
		t.Fatal(err)
	}
	committed, _ := s.List("alice", "p")
	cpu := 4
	restaged := editEntry("ns", "a")
	restaged.Edit.CPUCores = &cpu
	if err := s.Stage("alice", "p", restaged, editEntry("ns", "c")); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("alice", "p", committed...); err != nil {
		t.Fatal(err)
	}
	reopened, _ := Open(dir)
	got, _ := reopened.List("alice", "p")
	if len(got) != 2 || got[0].Name != "a" || got[0].Edit.CPUCores == nil || got[1].Name != "c" {
		t.Fatalf("want the restaged a and the new c kept, got %+v", got)
	}
}

// A write that fails stages nothing: memory must not hold what disk lacks.
func TestFailedWriteStagesNothing(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	// The draft file reads as absent but cannot be written: a directory holds
	// its temp name.
	if err := os.MkdirAll(filepath.Join(dir, "alice", "p.json.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Stage("alice", "p", editEntry("ns", "a"), editEntry("ns", "b")); err == nil {
		t.Fatal("want the write to fail")
	}
	if got, _ := s.List("alice", "p"); len(got) != 0 {
		t.Fatalf("a failed write staged %d entries", len(got))
	}
}

func TestReplaceResource(t *testing.T) {
	s, _ := Open(t.TempDir())
	drs := func(name string) Entry {
		return Entry{Kind: KindCreate, Resource: ResourceDRS, Namespace: "_cluster", Name: name, SourceFile: name + ".yaml", Manifest: "x"}
	}
	if err := s.Stage("alice", "p", drs("a"), drs("b"), editEntry("ns", "vm")); err != nil {
		t.Fatal(err)
	}
	removed, err := s.ReplaceResource("alice", "p", ResourceDRS, drs("c"))
	if err != nil || removed != 2 {
		t.Fatalf("ReplaceResource = %d, %v; want 2 dropped", removed, err)
	}
	got, _ := s.List("alice", "p")
	if len(got) != 2 || got[0].Name != "c" || got[1].Name != "vm" {
		t.Fatalf("want the VM edit and the new DRS file, got %+v", got)
	}
}

// A refused change stages nothing, not even the part fn made before refusing.
func TestApplyErrorStagesNothing(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Stage("alice", "p", editEntry("ns", "a")); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("refused")
	err := s.Apply("alice", "p", func(d map[string]Entry) error {
		delete(d, editEntry("ns", "a").Key())
		d[editEntry("ns", "b").Key()] = editEntry("ns", "b")
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("want fn's error, got %v", err)
	}
	if got, _ := s.List("alice", "p"); len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("want the draft as it was, got %+v", got)
	}
}

// A proposal record survives a restart and is dropped on request; a project
// whose name ends like the record's file never collides with it.
func TestProposalRecord(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if _, ok, err := s.Proposal("alice", "p"); ok || err != nil {
		t.Fatalf("want none, got ok=%v err=%v", ok, err)
	}
	want := Proposal{PR: 3, Head: "abc", Entries: []Entry{editEntry("ns", "a")}}
	if err := s.SetProposal("alice", "p", want); err != nil {
		t.Fatal(err)
	}
	if err := s.Stage("alice", "p.proposal", editEntry("ns", "b")); err != nil {
		t.Fatal(err)
	}
	reopened, _ := Open(dir)
	got, ok, err := reopened.Proposal("alice", "p")
	if err != nil || !ok || got.PR != 3 || got.Head != "abc" || len(got.Entries) != 1 {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	if err := reopened.DropProposal("alice", "p"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := reopened.Proposal("alice", "p"); ok {
		t.Fatal("a dropped proposal must read as none")
	}
	if d, _ := reopened.List("alice", "p.proposal"); len(d) != 1 {
		t.Fatalf("the look-alike project's draft must stand, got %+v", d)
	}
}
