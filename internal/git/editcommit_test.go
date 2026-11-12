package git

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
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
		[]ChangesetItem{{Path: "tenant-a/db.yaml", Delete: true}},
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
		[]ChangesetItem{{Path: "alpha/ghost.yaml", Delete: true}},
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
			{Path: "tenant-a/ghost.yaml", Delete: true},
			{Path: "tenant-a/db.yaml", Delete: true},
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

// A Transform runs on the file as base holds it in the fresh write clone, and
// its output is what lands on the branch.
func TestCommitChangesetTransformRewritesBase(t *testing.T) {
	bare := seedMultiRepo(t)
	w := OpenWrite(bare, "", nil, true)

	var seen []byte
	res, err := w.CommitChangeset("main", "dotvirt/proposed", "rename",
		[]ChangesetItem{{Path: "tenant-a/web.yaml", Transform: func(current []byte) ([]byte, error) {
			seen = current
			return bytes.ReplaceAll(current, []byte("name: web"), []byte("name: www")), nil
		}}},
		Author{Name: "u", Email: "u@x"})
	if err != nil {
		t.Fatalf("CommitChangeset: %v", err)
	}
	if !bytes.Contains(seen, []byte("name: web")) {
		t.Fatalf("Transform must see the base content, got %q", seen)
	}
	out, err := exec.Command("git", "--git-dir", bare, "show", res.Branch+":tenant-a/web.yaml").CombinedOutput()
	if err != nil {
		t.Fatalf("git show: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("name: www")) {
		t.Errorf("branch holds %q, want the transformed content", out)
	}

	boom := errors.New("boom")
	_, err = w.CommitChangeset("main", "dotvirt/proposed", "fail",
		[]ChangesetItem{{Path: "tenant-a/web.yaml", Transform: func([]byte) ([]byte, error) { return nil, boom }}},
		Author{Name: "u", Email: "u@x"})
	if !errors.Is(err, boom) {
		t.Fatalf("a failing Transform must surface, got %v", err)
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

// An item pinned to a base blob commits only while base still holds it.
func TestCommitChangesetChecksBaseBlob(t *testing.T) {
	bare := seedMultiRepo(t)
	w := OpenWrite(bare, "", nil, true)
	blob, err := exec.Command("git", "--git-dir", bare, "rev-parse", "main:tenant-a/web.yaml").Output()
	if err != nil {
		t.Fatal(err)
	}
	item := func(pin string) []ChangesetItem {
		return []ChangesetItem{{Path: "tenant-a/web.yaml", NewContent: []byte("kind: VirtualMachine\n"), BaseBlob: pin}}
	}
	var moved *BaseMovedError
	if _, err := w.CommitChangeset("main", "p", "m", item("0123456789012345678901234567890123456789"), Author{}); !errors.As(err, &moved) || moved.Path != "tenant-a/web.yaml" {
		t.Fatalf("want BaseMovedError for tenant-a/web.yaml, got %v", err)
	}
	if _, err := w.CommitChangeset("main", "p", "m", item(strings.TrimSpace(string(blob))), Author{}); err != nil {
		t.Fatalf("an unmoved base must commit: %v", err)
	}
}

// A leased rebuild replaces the branch only while it still holds the head
// dotvirt pushed: it rebuilds from base, never on the old head, and a branch
// moved or deleted since is ErrBranchMoved with nothing pushed.
func TestProposeChangesetLease(t *testing.T) {
	bare := seedMultiRepo(t)
	w := OpenWrite(bare, "", nil, true)
	by := Author{Name: "u", Email: "u@x"}
	drop := []ChangesetItem{{Path: "tenant-a/db.yaml", Delete: true}}
	first, err := w.CommitChangeset("main", "p", "drop db", drop, by)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.ProposeChangeset("main", "p", "drop db again", drop, by, PushPlan{Lease: first.Hash})
	if err != nil {
		t.Fatalf("a held lease must push: %v", err)
	}
	if parent := revParse(t, bare, second.Hash+"^"); parent != revParse(t, bare, "main") {
		t.Fatalf("the rebuild must sit on base, parent is %s", parent)
	}
	if _, err := w.ProposeChangeset("main", "p", "stale", drop, by, PushPlan{Lease: first.Hash}); !errors.Is(err, ErrBranchMoved) {
		t.Fatalf("a stale lease: want ErrBranchMoved, got %v", err)
	}
	if head := revParse(t, bare, "p"); head != second.Hash {
		t.Fatalf("a refused push must leave the branch, got %s", head)
	}
	if _, err := w.ProposeChangeset("main", "gone", "deleted", drop, by, PushPlan{Lease: first.Hash}); !errors.Is(err, ErrBranchMoved) {
		t.Fatalf("a deleted branch: want ErrBranchMoved, got %v", err)
	}
	if h, err := w.RemoteHead("p"); err != nil || h != second.Hash {
		t.Fatalf("RemoteHead(p) = %q, %v; want %s", h, err, second.Hash)
	}
	if h, err := w.RemoteHead("gone"); err != nil || h != "" {
		t.Fatalf("RemoteHead(gone) = %q, %v; want none", h, err)
	}
}

// BeforePush runs between the commit and the push; its error stops the push,
// and a branch moved under it is refused at the push.
func TestProposeChangesetBeforePush(t *testing.T) {
	bare := seedMultiRepo(t)
	w := OpenWrite(bare, "", nil, true)
	by := Author{Name: "u", Email: "u@x"}
	drop := []ChangesetItem{{Path: "tenant-a/db.yaml", Delete: true}}
	first, err := w.CommitChangeset("main", "p", "drop db", drop, by)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop")
	if _, err := w.ProposeChangeset("main", "p", "x", drop, by, PushPlan{Lease: first.Hash, BeforePush: func(string) error { return stop }}); !errors.Is(err, stop) {
		t.Fatalf("want the hook's error, got %v", err)
	}
	theirs := ""
	_, err = w.ProposeChangeset("main", "p", "x", drop, by, PushPlan{Lease: first.Hash, BeforePush: func(string) error {
		cmd := exec.Command("git", "--git-dir", bare, "commit-tree", "-m", "theirs", "main^{tree}")
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.Output()
		if err != nil {
			return err
		}
		theirs = strings.TrimSpace(string(out))
		return exec.Command("git", "--git-dir", bare, "update-ref", "refs/heads/p", theirs).Run()
	}})
	if !errors.Is(err, ErrBranchMoved) {
		t.Fatalf("want ErrBranchMoved, got %v", err)
	}
	if head := revParse(t, bare, "p"); head != theirs {
		t.Fatalf("the other writer's head must stand, got %s", head)
	}
}

// Both forms go-git reports a refused lease in, for the pushed ref only.
func TestLeaseRefused(t *testing.T) {
	for msg, want := range map[string]bool{
		"non-fast-forward update: refs/heads/p":               true,
		"command error on refs/heads/p: failed to update ref": true,
		"command error on refs/heads/q: failed to update ref": false,
		"authentication required":                             false,
	} {
		if got := leaseRefused(errors.New(msg), "p"); got != want {
			t.Errorf("%q: got %v, want %v", msg, got, want)
		}
	}
}

// A push the forge's hook declined is a refusal that names the protection,
// never a moved branch, whatever the lease.
func TestHookDeclinedIsABranchRefusal(t *testing.T) {
	declined := errors.New("command error on refs/heads/p: pre-receive hook declined")
	if !hookDeclined(declined, "p") || hookDeclined(declined, "q") {
		t.Error("hookDeclined must match the pushed ref's decline only")
	}
	if hookDeclined(errors.New("command error on refs/heads/p: failed to update ref"), "p") {
		t.Error("a refused lease is not a hook decline")
	}
	err := error(&BranchRefusedError{Branch: "p", Reason: "branch p is protected from force push"})
	if !errors.Is(err, model.ErrConflict) || errors.Is(err, ErrBranchMoved) {
		t.Errorf("err = %v: want a conflict, not a moved branch", err)
	}
	if !strings.Contains(err.Error(), "(branch p is protected from force push)") || !strings.Contains(err.Error(), "leave the proposal branches unprotected") {
		t.Errorf("err = %q", err)
	}
}

// The forge's pre-receive hook declining a proposal's push, with a lease or
// without, comes back as the refusal with the forge's own words.
func TestProposeChangesetHookDeclined(t *testing.T) {
	bare := seedMultiRepo(t)
	w := OpenWrite(bare, "", nil, true)
	by := Author{Name: "u", Email: "u@x"}
	drop := []ChangesetItem{{Path: "tenant-a/db.yaml", Delete: true}}
	first, err := w.CommitChangeset("main", "p", "drop db", drop, by)
	if err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'Forgejo: branch p is protected from force push' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, plan := range map[string]PushPlan{"lease": {Lease: first.Hash}, "rebuild": {}} {
		_, err := w.ProposeChangeset("main", "p", "again", drop, by, plan)
		var refused *BranchRefusedError
		if !errors.As(err, &refused) || errors.Is(err, ErrBranchMoved) {
			t.Fatalf("%s: err = %v, want a BranchRefusedError", name, err)
		}
		if refused.Reason != "Forgejo: branch p is protected from force push" {
			t.Errorf("%s: reason = %q", name, refused.Reason)
		}
	}
	if head := revParse(t, bare, "p"); head != first.Hash {
		t.Fatalf("the declined push must leave p at %s, got %s", first.Hash, head)
	}
}

// Only a protection is answered with unprotecting: a quota or a push
// permission declines the same way and is named as the forge said it.
func TestBranchRefusedAdvice(t *testing.T) {
	for reason, advised := range map[string]bool{
		"":                                      true,
		"branch p is protected from force push": true,
		"Quota exceeded":                        false,
		"User 'bot' is not allowed to push to repository 'o/r'.": false,
	} {
		msg := (&BranchRefusedError{Branch: "p", Reason: reason}).Error()
		if got := strings.Contains(msg, "leave the proposal branches unprotected"); got != advised {
			t.Errorf("%q: advice %v, want %v (%s)", reason, got, advised, msg)
		}
		if reason != "" && !strings.Contains(msg, "("+reason+")") {
			t.Errorf("%q: the forge's reason is missing from %q", reason, msg)
		}
	}
}

// The forge's refusal is the last line it wrote, past git's counters.
func TestLastRemoteLine(t *testing.T) {
	for out, want := range map[string]string{
		"": "",
		"Counting objects: 100% (3/3), done.\r\n":                                                         "",
		"Resolving deltas: 100% (1/1), done.\nremote: Forgejo: branch p is protected from force push\n\n": "Forgejo: branch p is protected from force push",
		"branch p is protected from force push\n":                                                         "branch p is protected from force push",
	} {
		if got := lastRemoteLine(out); got != want {
			t.Errorf("%q: got %q, want %q", out, got, want)
		}
	}
}

// A create pins the path absent: one created on base since is refused.
func TestCommitChangesetChecksAbsentPin(t *testing.T) {
	bare := seedMultiRepo(t)
	w := OpenWrite(bare, "", nil, true)
	item := func(path string) []ChangesetItem {
		return []ChangesetItem{{Path: path, NewContent: []byte("kind: VirtualMachine\n"), BaseBlob: AbsentBlob}}
	}
	var moved *BaseMovedError
	if _, err := w.CommitChangeset("main", "p", "m", item("tenant-a/web.yaml"), Author{}); !errors.As(err, &moved) {
		t.Fatalf("an existing path pinned absent: want BaseMovedError, got %v", err)
	}
	if _, err := w.CommitChangeset("main", "p", "m", item("tenant-a/new.yaml"), Author{}); err != nil {
		t.Fatalf("an absent path pinned absent must commit: %v", err)
	}
}

func revParse(t *testing.T, bare, rev string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "rev-parse", rev).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}
