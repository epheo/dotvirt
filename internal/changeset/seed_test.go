package changeset

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Shared git scaffolding for the changeset tests: one place builds the bare
// "project repo" fixtures every suite seeds against.

// gitRun executes one git command in wd under a hermetic identity.
func gitRun(t *testing.T, wd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = wd
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// writeWorkFile writes one file (creating parents) into a work clone.
func writeWorkFile(t *testing.T, work, path string, content []byte) {
	t.Helper()
	full := filepath.Join(work, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedWork creates a bare repo plus its work clone and commits files to main,
// returning both — bare doubles as the repo URL, work lets a caller layer
// further branches on top.
func seedWork(t *testing.T, files map[string][]byte) (bare, work string) {
	t.Helper()
	dir := t.TempDir()
	bare = filepath.Join(dir, "remote.git")
	work = filepath.Join(dir, "work")
	gitRun(t, dir, "init", "-q", "--bare", "-b", "main", bare)
	gitRun(t, dir, "init", "-q", "-b", "main", work)
	if len(files) == 0 {
		files = map[string][]byte{"README.md": []byte("seed\n")}
	}
	for path, content := range files {
		writeWorkFile(t, work, path, content)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-qm", "seed")
	gitRun(t, work, "remote", "add", "origin", bare)
	gitRun(t, work, "push", "-q", "origin", "main")
	return bare, work
}

// seedBareFiles creates a bare repo with the given files on main — a platform
// repo in whatever state a test needs.
func seedBareFiles(t *testing.T, files map[string][]byte) string {
	bare, _ := seedWork(t, files)
	return bare
}

// seedBare creates a bare repo with one VM manifest (alpha/web) on main.
func seedBare(t *testing.T) string {
	manifest := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Always\n"
	return seedBareFiles(t, map[string][]byte{"web.yaml": []byte(manifest)})
}
