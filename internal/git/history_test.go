package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// webYAML is a tiny VM manifest whose cpu count varies per commit, so an edit
// produces a real content diff to revert.
func webYAML(cores int) string {
	return fmt.Sprintf("kind: VirtualMachine\nmetadata: {name: web, namespace: tenant-a}\nspec: {cores: %d}\n", cores)
}

// seedHistory builds a bare repo on main with three commits: seed (adds
// web.yaml), bump (edits web.yaml), add-db (adds db.yaml) — enough to exercise
// History ordering and both RevertItems modes (restore an edit, delete an add).
func seedHistory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	run := func(wd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=alice@x",
			"GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=alice@x")
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

	write("tenant-a/web.yaml", webYAML(2))
	run(work, "add", "-A")
	run(work, "commit", "-qm", "seed tenant-a")

	write("tenant-a/web.yaml", webYAML(4))
	run(work, "add", "-A")
	run(work, "commit", "-qm", "bump web to 4 cpu")

	write("tenant-a/db.yaml", "kind: VirtualMachine\nmetadata: {name: db, namespace: tenant-a}\n")
	run(work, "add", "-A")
	run(work, "commit", "-qm", "add db")

	run(work, "remote", "add", "origin", bare)
	run(work, "push", "-q", "origin", "main")
	return bare
}

func TestHistoryOrderAndFields(t *testing.T) {
	r, err := Open(seedHistory(t), "", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, err := r.History("main", 25)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("want 3 commits, got %d", len(commits))
	}
	// Newest first.
	want := []string{"add db", "bump web to 4 cpu", "seed tenant-a"}
	for i, w := range want {
		if commits[i].Message != w {
			t.Errorf("commit[%d].Message = %q, want %q", i, commits[i].Message, w)
		}
		if commits[i].Merge {
			t.Errorf("commit[%d] should not be a merge", i)
		}
	}
	c := commits[0]
	if c.Author != "alice" {
		t.Errorf("Author = %q, want alice", c.Author)
	}
	if len(c.ShortHash) != 8 || !strings.HasPrefix(c.Hash, c.ShortHash) {
		t.Errorf("ShortHash %q must be the 8-char prefix of Hash %q", c.ShortHash, c.Hash)
	}
	if c.When == "" {
		t.Error("When should be populated")
	}
}

func TestHistoryRespectsLimit(t *testing.T) {
	r, err := Open(seedHistory(t), "", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, err := r.History("main", 2)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("limit not respected: got %d, want 2", len(commits))
	}
	if commits[0].Message != "add db" {
		t.Errorf("newest-first broken: got %q", commits[0].Message)
	}
}

func TestRevertItemsRestoresEditedFile(t *testing.T) {
	r, err := Open(seedHistory(t), "", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, _ := r.History("main", 25)
	bump := commits[1] // "bump web to 4 cpu" — edits web.yaml from 2 to 4 cpu
	items, err := r.RevertItems(bump.Hash)
	if err != nil {
		t.Fatalf("RevertItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Path != "tenant-a/web.yaml" || it.Delete {
		t.Fatalf("want a restore of tenant-a/web.yaml, got %+v", it)
	}
	if got := string(it.NewContent); got != webYAML(2) {
		t.Errorf("restored content = %q, want the pre-edit %q", got, webYAML(2))
	}
}

func TestRevertItemsDeletesAddedFile(t *testing.T) {
	r, err := Open(seedHistory(t), "", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, _ := r.History("main", 25)
	addDB := commits[0] // "add db" — introduced db.yaml, so reverting deletes it
	items, err := r.RevertItems(addDB.Hash)
	if err != nil {
		t.Fatalf("RevertItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Path != "tenant-a/db.yaml" || !items[0].Delete {
		t.Errorf("want a delete of tenant-a/db.yaml, got %+v", items[0])
	}
}

func TestRevertItemsRejectsRoot(t *testing.T) {
	r, err := Open(seedHistory(t), "", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, _ := r.History("main", 25)
	root := commits[len(commits)-1] // "seed tenant-a" — no parent to restore to
	if _, err := r.RevertItems(root.Hash); err == nil {
		t.Fatal("expected an error reverting the root commit, got nil")
	}
}
