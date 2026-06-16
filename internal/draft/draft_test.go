package draft

import (
	"os"
	"path/filepath"
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

	// Same VM name, different (user, project) tuples — must not collide.
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

	// Count aggregates across a user's projects.
	if n, err := s.Count("alice"); err != nil || n != 2 {
		t.Errorf("Count(alice) = %d (err %v), want 2", n, err)
	}
	if n, err := s.Count("bob"); err != nil || n != 1 {
		t.Errorf("Count(bob) = %d (err %v), want 1", n, err)
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
	if projects, err := s2.ListProjects("alice"); err != nil || len(projects) != 1 || projects[0] != "team-a" {
		t.Errorf("ListProjects(alice) = %v (err %v), want [team-a]", projects, err)
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
	if projects, _ := s.ListProjects("system:admin"); len(projects) != 1 || projects[0] != "team/with/slashes" {
		t.Errorf("ListProjects didn't unescape: %v", projects)
	}
}

// TestClearRemovesFile checks Clear (and an Unstage that empties) deletes the
// on-disk file rather than leaving an empty [], so ListProjects doesn't keep
// reporting emptied projects.
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
	if projects, _ := s.ListProjects("alice"); len(projects) != 0 {
		t.Errorf("ListProjects should be empty after Clear, got %v", projects)
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
