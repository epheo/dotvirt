package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seedMultiRepo creates a bare repo with a README + two VM manifests under
// tenant-a/ on main, plus a `seed` branch mirroring it, returning the bare path.
func seedMultiRepo(t *testing.T) string {
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
	// A non-default branch: a fresh clone materializes it only as a remote ref,
	// so committing to it exercises checkoutBranch's remote-ref path.
	run(work, "branch", "seed")
	run(work, "push", "-q", "origin", "seed")
	return bare
}

// Commits are additive: files absent from the set survive, and re-committing
// identical content is a no-op that never churns history.
func TestCommitAdditiveAndNoOp(t *testing.T) {
	bare := seedMultiRepo(t)
	w := OpenWrite(bare, "", nil, true)

	content := []byte("kind: VirtualMachine\nmetadata: {name: web, namespace: tenant-a}\n")
	files := []File{{Path: "tenant-a/web.yaml", Content: content}}
	res, err := w.Commit("seed", "sync", files)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Committed {
		t.Fatal("identical content must not commit")
	}

	files = []File{{Path: "tenant-a/new.yaml", Content: []byte("kind: VirtualMachine\nmetadata: {name: new, namespace: tenant-a}\n")}}
	res, err = w.Commit("seed", "sync", files)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !res.Committed {
		t.Fatal("expected a commit for the new file")
	}

	tree := lsTree(t, bare, "seed")
	for _, want := range []string{"tenant-a/new.yaml", "tenant-a/web.yaml", "tenant-a/db.yaml", "README.md"} {
		if !contains(tree, want) {
			t.Errorf("%s missing after additive commit", want)
		}
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
