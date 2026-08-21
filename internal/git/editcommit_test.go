package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// seedRepo creates a bare repo with one VM manifest on main, returning the bare
// repo path usable as a WriteRepo URL. Uses the git CLI for setup brevity.
func seedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")

	run := func(wd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	// -b main on the bare init too: without it HEAD points at the host git's
	// default branch (often an unborn master), and go-git's clone fails
	// "reference not found" on machines without init.defaultBranch=main.
	run(dir, "init", "-q", "--bare", "-b", "main", bare)
	run(dir, "init", "-q", "-b", "main", work)
	manifest := `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
spec:
  runStrategy: Always
  template:
    spec:
      domain:
        cpu:
          cores: 2
        memory:
          guest: 2Gi
`
	if err := os.WriteFile(filepath.Join(work, "web.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "-A")
	run(work, "commit", "-qm", "seed")
	run(work, "remote", "add", "origin", bare)
	run(work, "push", "-q", "origin", "main")
	return bare
}

// TestCommitChangesetDeleteRemovesFile verifies a Delete item removes that file on
// the working branch while leaving siblings untouched.
func TestCommitChangesetDeleteRemovesFile(t *testing.T) {
	bare := seedMultiRepo(t) // README + tenant-a/web.yaml + tenant-a/db.yaml on main
	w := OpenWrite(bare, "", nil, true)

	res, err := w.CommitChangeset("main", "dotvirt/proposed", "drop db",
		[]ChangesetItem{{Path: "tenant-a/db.yaml", Namespace: "tenant-a", Name: "db", Delete: true}},
		Author{Name: "u", Email: "u@x"})
	if err != nil {
		t.Fatalf("CommitChangeset: %v", err)
	}
	tree := lsTree(t, bare, res.Branch)
	if contains(tree, "tenant-a/db.yaml") {
		t.Error("db.yaml should have been removed")
	}
	if !contains(tree, "tenant-a/web.yaml") || !contains(tree, "README.md") {
		t.Error("unrelated files must be kept")
	}
}

// Deleting an already-absent path (the only item) is a SATISFIED intent, not a
// failure: the changeset just has nothing left to commit, and the caller gets
// the classified ErrNoChanges - never an internal error that wedges the draft.
func TestCommitChangesetDeleteAbsentNoop(t *testing.T) {
	bare := seedRepo(t) // only web.yaml on main
	w := OpenWrite(bare, "", nil, true)

	_, err := w.CommitChangeset("main", "dotvirt/proposed", "drop ghost",
		[]ChangesetItem{{Path: "alpha/ghost.yaml", Namespace: "alpha", Name: "ghost", Delete: true}},
		Author{Name: "u", Email: "u@x"})
	if !errors.Is(err, ErrNoChanges) {
		t.Fatalf("want ErrNoChanges, got %v", err)
	}
}

// A stale delete (file already gone from base) beside a real change must not
// poison the changeset: the stale item is skipped and the rest commits. Found
// live: a delete staged against a briefly-stale mirror 500'd every subsequent
// propose until the user guessed to unstage it.
func TestCommitChangesetSkipsStaleDelete(t *testing.T) {
	bare := seedMultiRepo(t) // README + tenant-a/web.yaml + tenant-a/db.yaml
	w := OpenWrite(bare, "", nil, true)

	res, err := w.CommitChangeset("main", "dotvirt/proposed", "stale delete + real delete",
		[]ChangesetItem{
			{Path: "tenant-a/ghost.yaml", Namespace: "tenant-a", Name: "ghost", Delete: true},
			{Path: "tenant-a/db.yaml", Namespace: "tenant-a", Name: "db", Delete: true},
		},
		Author{Name: "u", Email: "u@x"})
	if err != nil {
		t.Fatalf("CommitChangeset: %v", err)
	}
	tree := lsTree(t, bare, res.Branch)
	if contains(tree, "tenant-a/db.yaml") {
		t.Error("the real delete must still land")
	}
	if !contains(tree, "tenant-a/web.yaml") {
		t.Error("unrelated files must be kept")
	}
}

// TestCommitChangesetStampsRealTime guards the fix for epoch-dated commits: a
// changeset must carry a real (recent) author time so the history view shows when
// the change landed, not "1970".
func TestCommitChangesetStampsRealTime(t *testing.T) {
	bare := seedRepo(t)
	w := OpenWrite(bare, "", nil, true)

	if _, err := w.CommitChangeset("main", "dotvirt/proposed", "add thing",
		[]ChangesetItem{{Path: "alpha/new.yaml", NewContent: []byte("kind: VirtualMachine\n")}},
		Author{Name: "alice", Email: "alice@x"}); err != nil {
		t.Fatalf("CommitChangeset: %v", err)
	}

	out, err := exec.Command("git", "--git-dir", bare, "log", "-1", "--format=%at", "dotvirt/proposed").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Fatalf("parse author time %q: %v", out, err)
	}
	if ts < 1577836800 { // 2020-01-01: anything below means the old epoch placeholder
		t.Errorf("commit author time %d is the epoch placeholder; want a real time", ts)
	}
}
