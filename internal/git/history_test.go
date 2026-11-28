package git

import (
	"errors"
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

func revertItems(r *Repo, hash string) ([]ChangesetItem, error) {
	d, err := r.CommitDiff(hash)
	if err != nil {
		return nil, err
	}
	return d.RevertItems()
}

func TestRevertItemsRestoresEditedFile(t *testing.T) {
	r, err := Open(seedHistory(t), "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, _ := r.History("main", 25)
	bump := commits[1] // "bump web to 4 cpu" - edits web.yaml from 2 to 4 cpu
	items, err := revertItems(r, bump.Hash)
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
	items, err := revertItems(r, addDB.Hash)
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
	if _, err := revertItems(r, root.Hash); err == nil {
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
	items, err := revertItems(r, commits[0].Hash)
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

// A revision holds a merge once the commit the forge merged it with is that
// revision or an ancestor, whatever later subjects claim the PR: a follow-up
// "(#12)" on top moves nothing. A rebase merge's last commit counts the same.
// A commit off the first-parent line, or one the mirror lacks, has no answer.
func TestHoldsFollowsTheForgesMergeCommit(t *testing.T) {
	h := seedMerge(t)
	before, merge := revParse(t, h.bare, "main~1"), revParse(t, h.bare, "main")
	prHead := revParse(t, h.bare, "main^2")
	h.write("tenant-a/db.yaml", "kind: VirtualMachine\nmetadata: {name: db, namespace: tenant-a}\nspec: {}\n")
	h.run("carol", h.work, "commit", "-qam", "follow-up on the disk (#12)")
	h.run("carol", h.work, "push", "-q", "origin", "main")
	followUp := revParse(t, h.bare, "main")
	// A rebase merge: the PR's commit lands on main as is, no merge commit.
	h.run("dave", h.work, "checkout", "-qb", "rebased")
	h.write("tenant-a/web.yaml", webYAML(16))
	h.run("dave", h.work, "commit", "-qam", "web to 16 cpu")
	h.run("admin", h.work, "checkout", "-q", "main")
	h.run("admin", h.work, "merge", "-q", "--ff-only", "rebased")
	h.run("admin", h.work, "push", "-q", "origin", "main")
	rebased := revParse(t, h.bare, "main")
	r := openAt(t, h.bare, "")

	for _, c := range []struct {
		what, commit, rev string
		holds, known      bool
	}{
		{"#12 before its merge", merge, before, false, true},
		{"#12 at its merge", merge, merge, true, true},
		{"#12 under a follow-up claiming it", merge, followUp, true, true},
		{"the follow-up, Argo at #12", followUp, merge, false, true},
		{"a rebase merge before it", rebased, followUp, false, true},
		{"a rebase merge at it", rebased, rebased, true, true},
		{"a PR head off the first-parent line", prHead, rebased, false, false},
		{"a revision the mirror lacks", merge, strings.Repeat("f", 40), false, false},
		{"no merge commit recorded", "", rebased, false, false},
	} {
		holds, known := r.Holds("main", c.commit, c.rev)
		if holds != c.holds || known != c.known {
			t.Errorf("%s: Holds = %v, %v; want %v, %v", c.what, holds, known, c.holds, c.known)
		}
	}
	// Asked again, the answer comes from the memo, the same.
	if holds, known := r.Holds("main", merge, before); holds || !known {
		t.Errorf("memoized answer = %v, %v", holds, known)
	}
}

// A folder's head is the last commit that touched it: a merge elsewhere in
// the repo moves the repo's head and leaves the folder's where it was.
func TestFolderHeadMovesWithItsFolderAlone(t *testing.T) {
	h := seedHistoryRepo(t)
	h.write("tenant-b/db.yaml", "kind: VirtualMachine\nmetadata: {name: db, namespace: tenant-b}\n")
	h.run("bob", h.work, "add", "-A")
	h.run("bob", h.work, "commit", "-qm", "add tenant-b")
	h.run("bob", h.work, "push", "-q", "origin", "main")

	folder, whole := openAt(t, h.bare, "tenant-a"), openAt(t, h.bare, "")
	if got, want := folder.FolderHead("main"), revParse(t, h.bare, "main~1"); got != want {
		t.Errorf("tenant-a head = %q, want its last commit %q", got, want)
	}
	if got, want := whole.FolderHead("main"), revParse(t, h.bare, "main"); got != want {
		t.Errorf("repo head = %q, want main %q", got, want)
	}
	if got := folder.FolderHead("nope"); got != "" {
		t.Errorf("a missing branch has head %q", got)
	}

	h.write("tenant-a/web.yaml", webYAML(8))
	h.run("alice", h.work, "commit", "-qam", "bump web to 8 cpu")
	h.run("alice", h.work, "push", "-q", "origin", "main")
	if err := folder.refresh(); err != nil {
		t.Fatal(err)
	}
	if got, want := folder.FolderHead("main"), revParse(t, h.bare, "main"); got != want {
		t.Errorf("after a tenant-a merge, head = %q, want %q", got, want)
	}
}

// A file's history is the first-parent commits that changed it: the merge
// counts once for the PR, a sibling file's commits do not appear, and a file
// that never existed has none.
func TestFileHistoryFollowsOneFile(t *testing.T) {
	r, err := Open(seedMerge(t).bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	web, err := r.FileHistory("main", "tenant-a/web.yaml", 25)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(web) != 3 || !web[0].Merge || web[1].Message != "bump web to 4 cpu" || web[2].Message != "seed tenant-a" {
		t.Fatalf("web.yaml history = %+v", web)
	}
	db, err := r.FileHistory("main", "tenant-a/db.yaml", 25)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(db) != 1 || db[0].Message != "add db" {
		t.Errorf("db.yaml history = %+v, want only its add", db)
	}
	cache, err := r.FileHistory("main", "tenant-a/cache.yaml", 25)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(cache) != 1 || !cache[0].Merge {
		t.Errorf("cache.yaml history = %+v, want only the merge that added it", cache)
	}
	none, err := r.FileHistory("main", "tenant-a/missing.yaml", 25)
	if err != nil || len(none) != 0 {
		t.Errorf("missing file: %v %+v", err, none)
	}
}

// Only main's own first-parent commits are main's history: a commit pushed
// to a proposal branch, and the inside of a merged PR, are not.
func TestOnBranch(t *testing.T) {
	h := seedMerge(t)
	run, write, work := h.run, h.write, h.work
	run("eve", work, "checkout", "-qb", "dotvirt/proposed/eve/p-1")
	write("tenant-a/web.yaml", webYAML(64))
	run("eve", work, "commit", "-qam", "proposal")
	run("eve", work, "push", "-q", "origin", "dotvirt/proposed/eve/p-1")
	head := func(rev string) string {
		out, err := exec.Command("git", "-C", work, "rev-parse", rev).Output()
		if err != nil {
			t.Fatalf("rev-parse %s: %v", rev, err)
		}
		return strings.TrimSpace(string(out))
	}
	r, err := Open(h.bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for rev, want := range map[string]bool{
		"main":                     true,
		"main^":                    true,
		"main~3":                   true,
		"main^2":                   false,
		"dotvirt/proposed/eve/p-1": false,
	} {
		got, err := r.OnBranch("main", head(rev))
		if err != nil || got != want {
			t.Errorf("OnBranch(%s) = %v, %v; want %v", rev, got, err, want)
		}
	}
}

// A branch reviews for its proposer only while every commit past its fork is
// dotvirt's for them; main moving past the fork changes nothing. Another
// author's commit, a forge-side rebase (another committer), a merge of main
// into the branch or any merge at all makes it someone else's.
func TestBranchDiffBy(t *testing.T) {
	h := seedHistoryRepo(t)
	alice := Author{Name: "alice", Email: "alice@dotvirt.noreply"}
	dotvirt := Author{Name: "dotvirt", Email: "dotvirt@localhost"}
	bob := Author{Name: "bob", Email: "bob@x"}
	git := func(author, committer Author, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = h.work
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME="+author.Name, "GIT_AUTHOR_EMAIL="+author.Email,
			"GIT_COMMITTER_NAME="+committer.Name, "GIT_COMMITTER_EMAIL="+committer.Email)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(author, committer Author, from, branch, path, content string) {
		t.Helper()
		git(author, committer, "checkout", "-qB", branch, from)
		h.write(path, content)
		git(author, committer, "add", "-A")
		git(author, committer, "commit", "-qm", branch)
		git(author, committer, "push", "-qf", "origin", branch)
	}
	web := "tenant-a/web.yaml"
	commit(alice, dotvirt, "main", "own", web, webYAML(16))
	commit(bob, bob, "own", "pushed-to", web, webYAML(32))
	commit(alice, bob, "main", "rebased", web, webYAML(16))
	commit(alice, dotvirt, "main", "updated", web, webYAML(16))
	// main moves past every fork
	commit(alice, dotvirt, "main", "main", "tenant-a/db.yaml", "kind: VirtualMachine\nmetadata: {name: db, namespace: tenant-a}\nspec: {cores: 2}\n")
	git(bob, bob, "checkout", "-q", "updated")
	git(bob, bob, "merge", "-q", "--no-ff", "-m", "update branch", "main")
	git(bob, bob, "push", "-q", "origin", "updated")
	// A merge signed as dotvirt's for alice still brings in bob's commit.
	commit(bob, bob, "main", "side", "tenant-a/side.yaml", "kind: VirtualMachine\nmetadata: {name: side, namespace: tenant-a}\n")
	commit(alice, dotvirt, "main", "smuggled", web, webYAML(16))
	git(alice, dotvirt, "merge", "-q", "--no-ff", "-m", "smuggle", "side")
	git(alice, dotvirt, "push", "-q", "origin", "smuggled")

	r, err := Open(h.bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	files, at, err := r.BranchDiffBy("main", "own", alice)
	if err != nil {
		t.Fatalf("own branch: %v", err)
	}
	if len(files) != 1 || files[0].Path != "tenant-a/web.yaml" || !strings.Contains(string(files[0].After), "cores: 16") || !strings.Contains(string(files[0].Before), "cores: 4") {
		t.Errorf("own branch diff = %+v, want web from its fork, without main's later db edit", files)
	}
	if at != r.BranchHash("own") {
		t.Errorf("at = %q, want the head diffed %q", at, r.BranchHash("own"))
	}
	for _, c := range []struct {
		branch string
		by     Author
	}{{"own", bob}, {"pushed-to", alice}, {"rebased", alice}, {"updated", alice}, {"smuggled", alice}} {
		if files, _, err := r.BranchDiffBy("main", c.branch, c.by); !errors.Is(err, ErrForeignCommit) || files != nil {
			t.Errorf("%s for %s: %v and %d files, want ErrForeignCommit and no diff", c.branch, c.by.Name, err, len(files))
		}
	}
	if _, _, err := r.BranchDiffBy("main", "missing", alice); !errors.Is(err, ErrNoBranch) {
		t.Errorf("missing branch: %v, want ErrNoBranch", err)
	}
}

// A user who commits their own changes reads their branch whether they or
// dotvirt committed it; the committer must still be one of the two, and a
// user who does not commit reads only dotvirt's commits.
func TestBranchDiffByCommittingAuthor(t *testing.T) {
	h := seedHistoryRepo(t)
	alice := Author{Name: "alice", Email: "alice@dotvirt.noreply"}
	aliceCommits := Author{Name: alice.Name, Email: alice.Email, Commits: true}
	dotvirt := Author{Name: "dotvirt", Email: "dotvirt@localhost"}
	bob := Author{Name: "bob", Email: "bob@x"}
	commit := func(author, committer Author, branch string) {
		t.Helper()
		for _, args := range [][]string{{"checkout", "-qB", branch, "main"}, {"add", "-A"}, {"commit", "-qm", branch}, {"push", "-qf", "origin", branch}} {
			if args[0] == "add" {
				h.write("tenant-a/web.yaml", webYAML(16))
			}
			cmd := exec.Command("git", args...)
			cmd.Dir = h.work
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME="+author.Name, "GIT_AUTHOR_EMAIL="+author.Email,
				"GIT_COMMITTER_NAME="+committer.Name, "GIT_COMMITTER_EMAIL="+committer.Email)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}
	commit(alice, alice, "self")
	commit(alice, dotvirt, "bot")
	commit(alice, bob, "other")

	r, err := Open(h.bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, c := range []struct {
		branch string
		by     Author
		reads  bool
	}{
		{"self", aliceCommits, true},
		{"bot", aliceCommits, true},
		{"other", aliceCommits, false},
		{"self", alice, false},
		{"bot", alice, true},
	} {
		_, _, err := r.BranchDiffBy("main", c.branch, c.by)
		if got := err == nil; got != c.reads || err != nil && !errors.Is(err, ErrForeignCommit) {
			t.Errorf("%s for %+v: %v, want reads=%v", c.branch, c.by, err, c.reads)
		}
	}
}

// Descends tells git moving on by commits from a history that replaced what
// was there, through any parent, and says when the mirror cannot tell yet.
func TestDescendsTellsMovingOnFromReplaced(t *testing.T) {
	h := seedMerge(t)
	r, err := Open(h.bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	merge, addDB, seed := revParse(t, h.bare, "main"), revParse(t, h.bare, "main~1"), revParse(t, h.bare, "main~3")
	inside := revParse(t, h.bare, "main^2")
	check := func(name, newer, older string, follows, known bool) {
		t.Helper()
		f, k, err := r.Descends("main", newer, older)
		if err != nil || f != follows || k != known {
			t.Errorf("%s: follows=%v known=%v err=%v, want %v %v", name, f, k, err, follows, known)
		}
	}
	check("moved on by a merge", merge, addDB, true, true)
	check("moved on by commits", addDB, seed, true, true)
	check("the same revision", merge, merge, true, true)
	check("older is newer", addDB, merge, false, true)
	check("older inside a merged PR", merge, inside, true, true)
	check("newer not fetched", strings.Repeat("a", 40), seed, false, false)

	// A merge whose first parent is the PR's side still moved on from main.
	h.run("bob", h.work, "checkout", "-qb", "side", "main~1")
	h.write("tenant-a/side.yaml", "kind: VirtualMachine\nmetadata: {name: side, namespace: tenant-a}\n")
	h.run("bob", h.work, "add", "-A")
	h.run("bob", h.work, "commit", "-qm", "side")
	h.run("bob", h.work, "merge", "-q", "--no-ff", "-m", "merge main into side", "main")
	h.run("bob", h.work, "push", "-qf", "origin", "side:main")
	if err := r.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	check("first parent on the PR side", revParse(t, h.bare, "main"), merge, true, true)

	// The forge loses the repo and it comes back with a history of its own.
	h.run("admin", h.work, "checkout", "-q", "--orphan", "fresh")
	h.run("admin", h.work, "rm", "-rqf", ".")
	h.write("README.md", "recovered\n")
	h.write("templates/web.yaml", "kind: VirtualMachineTemplate\n")
	h.run("admin", h.work, "add", "-A")
	h.run("admin", h.work, "commit", "-qm", "re-created")
	h.run("admin", h.work, "push", "-qf", "origin", "fresh:main")
	if err := r.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	check("replaced history", revParse(t, h.bare, "main"), merge, false, true)

	// The replaced history joined back to the old one, keeping its own tree:
	// the old main is an ancestor again, through that merge alone.
	h.run("admin", h.work, "merge", "-q", "-s", "ours", "--allow-unrelated-histories", "-m", "rejoin", "main")
	h.run("admin", h.work, "push", "-qf", "origin", "fresh:main")
	if err := r.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	check("replaced, then rejoined", revParse(t, h.bare, "main"), merge, false, true)
}

// DeclaresAt reads git as Argo's source renders it: an object (a kind and a
// name) in a .yaml file outside the template library. A .yml file, a
// comment-only .yaml, a nameless document and the library declare nothing.
func TestDeclaresAtReadsWhatArgoRenders(t *testing.T) {
	h := seedHistoryRepo(t)
	h.run("alice", h.work, "rm", "-rq", "tenant-a")
	h.write("templates/web.yaml", "kind: VirtualMachineTemplate\nmetadata: {name: web}\n")
	h.write("README.md", "nothing declared\n")
	h.write(".forgejo/workflows/ci.yml", "kind: VirtualMachine\nmetadata: {name: ci}\n")
	h.write("notes.yaml", "# nothing here yet\n")
	h.write("nameless.yaml", "kind: VirtualMachine\nmetadata: {}\n")
	h.run("alice", h.work, "add", "-A")
	h.run("alice", h.work, "commit", "-qm", "delete everything")
	h.run("alice", h.work, "push", "-q", "origin", "main")
	r, err := Open(h.bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, c := range []struct {
		rev      string
		declares bool
	}{{"main~1", true}, {"main", false}} {
		got, err := r.DeclaresAt(revParse(t, h.bare, c.rev))
		if err != nil || got != c.declares {
			t.Errorf("%s: declares=%v err=%v, want %v", c.rev, got, err, c.declares)
		}
	}
	scoped, err := Open(h.bare, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	scoped.root = "elsewhere"
	if got, err := scoped.DeclaresAt(revParse(t, h.bare, "main~1")); err != nil || got {
		t.Errorf("a folder holding nothing: declares=%v err=%v, want false", got, err)
	}
}
