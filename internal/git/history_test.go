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

// historyRepo is a bare repo plus the work clone that feeds it; run executes
// git in a directory as the named author, write puts a file in the clone.
type historyRepo struct {
	bare, work string
	run        func(as, wd string, args ...string)
	write      func(path, content string)
}

// seedHistory builds a bare repo on main with three commits: seed (adds
// web.yaml), bump (edits web.yaml), add-db (adds db.yaml) - enough to exercise
// History ordering and both RevertItems modes (restore an edit, delete an add).
func seedHistory(t *testing.T) string {
	return seedHistoryRepo(t).bare
}

func seedHistoryRepo(t *testing.T) historyRepo {
	t.Helper()
	dir := t.TempDir()
	h := historyRepo{bare: filepath.Join(dir, "remote.git"), work: filepath.Join(dir, "work")}
	h.run = func(as, wd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME="+as, "GIT_AUTHOR_EMAIL="+as+"@x",
			"GIT_COMMITTER_NAME="+as, "GIT_COMMITTER_EMAIL="+as+"@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	h.write = func(path, content string) {
		full := filepath.Join(h.work, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run, write, work := h.run, h.write, h.work
	run("alice", dir, "init", "-q", "--bare", "-b", "main", h.bare)
	run("alice", dir, "init", "-q", "-b", "main", work)

	write("tenant-a/web.yaml", webYAML(2))
	run("alice", work, "add", "-A")
	run("alice", work, "commit", "-qm", "seed tenant-a")

	write("tenant-a/web.yaml", webYAML(4))
	run("alice", work, "add", "-A")
	run("alice", work, "commit", "-qm", "bump web to 4 cpu")

	write("tenant-a/db.yaml", "kind: VirtualMachine\nmetadata: {name: db, namespace: tenant-a}\n")
	run("alice", work, "add", "-A")
	run("alice", work, "commit", "-qm", "add db")

	run("alice", work, "remote", "add", "origin", h.bare)
	run("alice", work, "push", "-q", "origin", "main")
	return h
}

// seedMerge layers a forge-style PR merge on seedHistory: bob's branch bumps web
// to 8 cpu and adds cache.yaml, and admin merges it with the subject Forgejo
// writes. The merge commit is what every dotvirt change looks like on main.
func seedMerge(t *testing.T) historyRepo {
	t.Helper()
	h := seedHistoryRepo(t)
	run, write, work := h.run, h.write, h.work
	run("bob", work, "checkout", "-qb", "feature")
	write("tenant-a/web.yaml", webYAML(8))
	write("tenant-a/cache.yaml", "kind: VirtualMachine\nmetadata: {name: cache, namespace: tenant-a}\n")
	run("bob", work, "add", "-A")
	run("bob", work, "commit", "-qm", "bump web to 8 cpu, add cache")
	run("admin", work, "checkout", "-q", "main")
	run("admin", work, "merge", "-q", "--no-ff", "-m",
		"Merge pull request 'Resize web' (#12) from dotvirt/proposed/bob/p-1a2b into main", "feature")
	run("admin", work, "push", "-q", "origin", "main")
	return h
}

func TestHistoryOrderAndFields(t *testing.T) {
	r, err := Open(seedHistory(t), "", nil)
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
	r, err := Open(seedHistory(t), "", nil)
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
	r, err := Open(seedHistory(t), "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, _ := r.History("main", 25)
	bump := commits[1] // "bump web to 4 cpu" - edits web.yaml from 2 to 4 cpu
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
	r, err := Open(seedHistory(t), "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, _ := r.History("main", 25)
	addDB := commits[0] // "add db" - introduced db.yaml, so reverting deletes it
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
	r, err := Open(seedHistory(t), "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, _ := r.History("main", 25)
	root := commits[len(commits)-1] // "seed tenant-a" - no parent to restore to
	if _, err := r.RevertItems(root.Hash); err == nil {
		t.Fatal("expected an error reverting the root commit, got nil")
	}
}

// A forge merge is the shape of every dotvirt change on main. It must be
// attributed to the change's author (the merged branch), not the merger, and
// revert against the base branch side - undoing the whole PR.
func TestHistoryAttributesMergeToHeadAuthor(t *testing.T) {
	r, err := Open(seedMerge(t).bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, err := r.History("main", 25)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	m := commits[0]
	if !m.Merge {
		t.Fatalf("newest commit should be the merge, got %+v", m)
	}
	// First parents only: the merge, then main's three commits; bob's branch
	// commit is the PR's internals, not history.
	if len(commits) != 4 || commits[len(commits)-1].Message != "seed tenant-a" {
		t.Errorf("want the first-parent chain (4 commits ending at the root), got %d: %+v", len(commits), commits)
	}
	for _, c := range commits {
		if c.Message == "bump web to 8 cpu, add cache" {
			t.Errorf("the merged branch's own commit must not appear in history")
		}
	}
	if m.Author != "bob" {
		t.Errorf("merge Author = %q, want the merged branch's author bob", m.Author)
	}
	if !strings.HasPrefix(m.Message, "Merge pull request 'Resize web' (#12)") {
		t.Errorf("Message should be the subject as committed, got %q", m.Message)
	}
}

func TestRevertItemsUndoesWholeMerge(t *testing.T) {
	r, err := Open(seedMerge(t).bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, _ := r.History("main", 25)
	items, err := r.RevertItems(commits[0].Hash)
	if err != nil {
		t.Fatalf("RevertItems(merge): %v", err)
	}
	got := map[string]ChangesetItem{}
	for _, it := range items {
		got[it.Path] = it
	}
	if len(got) != 2 {
		t.Fatalf("want web restored + cache deleted, got %+v", items)
	}
	if web := got["tenant-a/web.yaml"]; web.Delete || string(web.NewContent) != webYAML(4) {
		t.Errorf("web.yaml should restore to the pre-merge 4 cpu, got %+v", web)
	}
	if cache := got["tenant-a/cache.yaml"]; !cache.Delete {
		t.Errorf("cache.yaml was added by the merge, so the revert must delete it: %+v", cache)
	}
}

func TestCommitDiffSides(t *testing.T) {
	r, err := Open(seedMerge(t).bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, _ := r.History("main", 25)
	d, err := r.CommitDiff(commits[0].Hash)
	if err != nil {
		t.Fatalf("CommitDiff(merge): %v", err)
	}
	if d.Commit.Hash != commits[0].Hash || len(d.Files) != 2 {
		t.Fatalf("got %+v", d)
	}
	if d.Files[0].Path != "tenant-a/cache.yaml" || d.Files[0].Before != nil || d.Files[0].After == nil {
		t.Errorf("added file must carry After only: %+v", d.Files[0])
	}
	if f := d.Files[1]; f.Path != "tenant-a/web.yaml" || string(f.Before) != webYAML(4) || string(f.After) != webYAML(8) {
		t.Errorf("edited file must carry both sides: %+v", f)
	}

	root := commits[len(commits)-1]
	d, err = r.CommitDiff(root.Hash)
	if err != nil {
		t.Fatalf("CommitDiff(root): %v", err)
	}
	if len(d.Files) != 1 || d.Files[0].Before != nil || string(d.Files[0].After) != webYAML(2) {
		t.Errorf("root diffs against the empty tree, got %+v", d.Files)
	}
	if _, err := r.CommitDiff("0123456789abcdef0123456789abcdef01234567"); err == nil {
		t.Error("an unknown hash must error")
	}
}
