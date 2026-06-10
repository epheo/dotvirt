package git

import (
	"os"
	"os/exec"
	"path/filepath"
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

	run(dir, "init", "-q", "--bare", bare)
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

func TestCommitVMEditCreatesFeatureBranch(t *testing.T) {
	bare := seedRepo(t)
	repo := OpenWrite(bare, "", "", true)

	mem := "4Gi"
	res, err := repo.CommitVMEdit("main", "dotvirt/edit-web-1", "web.yaml", "alpha", "web",
		"dotvirt: edit alpha/web (memory=4Gi)", VMEdit{Memory: &mem})
	if err != nil {
		t.Fatalf("CommitVMEdit: %v", err)
	}

	if res.Branch != "dotvirt/edit-web-1" {
		t.Errorf("branch = %q", res.Branch)
	}
	if !strings.Contains(res.Diff, "-          guest: 2Gi") || !strings.Contains(res.Diff, "+          guest: 4Gi") {
		t.Errorf("diff not minimal/expected:\n%s", res.Diff)
	}

	// main must be untouched; the feature branch must carry the change.
	if got := showFile(t, bare, "main", "web.yaml"); strings.Contains(got, "4Gi") {
		t.Error("source branch main was modified — edit leaked off the feature branch")
	}
	if got := showFile(t, bare, "dotvirt/edit-web-1", "web.yaml"); !strings.Contains(got, "guest: 4Gi") {
		t.Error("feature branch missing the edit")
	}
}

func showFile(t *testing.T, bare, ref, path string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", bare, "show", ref+":"+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git show %s:%s: %v\n%s", ref, path, err, out)
	}
	return string(out)
}
