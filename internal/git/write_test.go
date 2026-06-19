package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seedRunning creates a bare repo with a README + two VM manifests under tenant-a/
// on a `running` branch, returning the bare path.
func seedRunning(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	run := func(wd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, content string) {
		full := filepath.Join(work, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(dir, "init", "-q", "--bare", "-b", "main", bare)
	run(dir, "init", "-q", "-b", "main", work)
	write("README.md", "# project\n")
	write("tenant-a/web.yaml", "kind: VirtualMachine\nmetadata: {name: web, namespace: tenant-a}\n")
	write("tenant-a/db.yaml", "kind: VirtualMachine\nmetadata: {name: db, namespace: tenant-a}\n")
	run(work, "add", "-A")
	run(work, "commit", "-qm", "seed")
	run(work, "remote", "add", "origin", bare)
	run(work, "push", "-q", "origin", "main")
	// running mirrors main initially (as the platform seeds it).
	run(work, "branch", "running")
	run(work, "push", "-q", "origin", "running")
	return bare
}

// TestCommitPrunesStaleManaged verifies that a managed-dir commit removes files
// no longer present in the new set (a VM deleted from the cluster), while leaving
// files outside the managed dirs (README) untouched.
func TestCommitPrunesStaleManaged(t *testing.T) {
	bare := seedRunning(t)
	w := OpenWrite(bare, "", nil, true)

	// Export now sees only web (db was deleted from the cluster).
	files := []File{{Path: "tenant-a/web.yaml", Content: []byte("kind: VirtualMachine\nmetadata: {name: web, namespace: tenant-a}\n")}}
	res, err := w.Commit("running", "sync", files, []string{"tenant-a"})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !res.Committed {
		t.Fatal("expected a commit (db.yaml should have been pruned)")
	}

	tree := lsTree(t, bare, "running")
	if !contains(tree, "tenant-a/web.yaml") {
		t.Error("web.yaml should remain")
	}
	if contains(tree, "tenant-a/db.yaml") {
		t.Error("db.yaml should have been pruned (VM deleted from cluster)")
	}
	if !contains(tree, "README.md") {
		t.Error("README.md is outside managed dirs and must be kept")
	}
}

// TestCommitNoPruneWithoutManaged keeps the additive behavior when no managed dirs
// are given: stale files are NOT removed.
func TestCommitNoPruneWithoutManaged(t *testing.T) {
	bare := seedRunning(t)
	w := OpenWrite(bare, "", nil, true)

	files := []File{{Path: "tenant-a/web.yaml", Content: []byte("kind: VirtualMachine\nmetadata: {name: web, namespace: tenant-a}\n")}}
	if _, err := w.Commit("running", "sync", files, nil); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if tree := lsTree(t, bare, "running"); !contains(tree, "tenant-a/db.yaml") {
		t.Error("db.yaml must survive when no managed dirs are passed (additive commit)")
	}
}

func lsTree(t *testing.T, bare, ref string) []string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", bare, "ls-tree", "-r", "--name-only", ref)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ls-tree: %v\n%s", err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
