package git

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/model"
)

// A repo where tenant-a/ is the project and other/ is someone else's.
func seedFolderRepo(t *testing.T) historyRepo {
	t.Helper()
	h := seedHistoryRepo(t)
	h.write("other/app.yaml", "kind: Deployment\n")
	h.run("alice", h.work, "add", "-A")
	h.run("alice", h.work, "commit", "-qm", "someone else's app")
	h.run("alice", h.work, "push", "-q", "origin", "main")
	return h
}

func openAt(t *testing.T, bare, root string) *Repo {
	t.Helper()
	r, err := Open(bare, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.root = root
	return r
}

func walked(t *testing.T, r *Repo) []string {
	t.Helper()
	got := []string{}
	err := r.WalkYAML("main", func(string) bool { return true }, func(p string, _ []byte) error {
		got = append(got, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	return got
}

func TestFolderReadsSeeOnlyTheFolder(t *testing.T) {
	h := seedFolderRepo(t)
	r := openAt(t, h.bare, "tenant-a")
	if got := walked(t, r); !slices.Equal(got, []string{"db.yaml", "web.yaml"}) {
		t.Errorf("walk = %v", got)
	}
	if _, err := r.FileOnBranch("main", "web.yaml"); err != nil {
		t.Errorf("FileOnBranch: %v", err)
	}
	if _, ok, err := r.LookupOnBranch("main", "../other/app.yaml"); ok || err != nil {
		t.Errorf("reached outside the folder: ok=%v err=%v", ok, err)
	}
	commits, err := r.History("main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 {
		t.Fatalf("history = %d commits, want the folder's 3", len(commits))
	}
	diff, err := r.CommitDiff(commits[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "db.yaml" {
		t.Errorf("diff = %+v, want the path inside the folder", diff.Files)
	}
	if _, err := r.FileAt(commits[0].Hash, "db.yaml"); err != nil {
		t.Errorf("FileAt: %v", err)
	}
}

// A project can be pointed at a folder before anything lands in it.
func TestAFolderTheBranchLacksReadsEmpty(t *testing.T) {
	h := seedFolderRepo(t)
	r := openAt(t, h.bare, "not/yet")
	if got := walked(t, r); len(got) != 0 {
		t.Errorf("walk = %v", got)
	}
	if _, ok, err := r.LookupOnBranch("main", "web.yaml"); ok || err != nil {
		t.Errorf("lookup: ok=%v err=%v", ok, err)
	}
}

func folderWriter(bare string) *WriteRepo {
	w := OpenWrite(bare, "", nil, true)
	w.root = "tenant-a"
	return w
}

func TestFolderWritesLandInTheFolder(t *testing.T) {
	h := seedFolderRepo(t)
	if _, err := folderWriter(h.bare).Commit("main", "add", []model.File{{Path: "net/x.yaml", Content: []byte("a: 1\n")}}); err != nil {
		t.Fatal(err)
	}
	files := lsTree(t, h.bare, "main")
	if !contains(files, "tenant-a/net/x.yaml") || contains(files, "net/x.yaml") {
		t.Errorf("tree = %v", files)
	}
}

func TestAWriteCannotLeaveTheFolder(t *testing.T) {
	h := seedFolderRepo(t)
	for name, it := range map[string]ChangesetItem{
		"write":  {Path: "../other/app.yaml", NewContent: []byte("taken: true\n")},
		"delete": {Path: "../other/app.yaml", Delete: true},
	} {
		_, err := folderWriter(h.bare).CommitChangeset("main", "feature", "m", []ChangesetItem{it}, Author{})
		if !errors.Is(err, model.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// Propose matches a moved file against the staged paths, which are the folder's.
func TestAMovedPinNamesThePathInsideTheFolder(t *testing.T) {
	h := seedFolderRepo(t)
	it := ChangesetItem{Path: "web.yaml", NewContent: []byte("a: 1\n"), BaseBlob: AbsentBlob}
	_, err := folderWriter(h.bare).CommitChangeset("main", "feature", "m", []ChangesetItem{it}, Author{})
	var moved *BaseMovedError
	if !errors.As(err, &moved) || moved.Path != "web.yaml" {
		t.Errorf("err = %v", err)
	}
}

func TestGetAtScopesThePair(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	first, firstW, err := rs.GetAt(bare, "a")
	if err != nil {
		t.Fatal(err)
	}
	if first.root != "a" || firstW.root != "a" {
		t.Errorf("roots = %q, %q", first.root, firstW.root)
	}
	if again, _, _ := rs.GetAt(bare, "a"); again != first {
		t.Error("the same folder re-opened the repo")
	}
	moved, _, err := rs.GetAt(bare, "b")
	if err != nil {
		t.Fatal(err)
	}
	if moved == first || moved.root != "b" {
		t.Errorf("a moved folder kept the old pair: root %q", moved.root)
	}
}
