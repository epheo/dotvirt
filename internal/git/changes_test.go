package git

import (
	"errors"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// offerSet is the file set the cases below commit as the offer.
var offerSet = []model.File{
	{Path: "templates/a.yaml", Content: []byte("kind: VirtualMachineTemplate\n")},
	{Path: "templates/.marker", Content: []byte("offered\n")},
}

// offerRepo is seedHistory with branch offer checked out: offerSet as one
// commit on main.
func offerRepo(t *testing.T) historyRepo {
	t.Helper()
	h := seedHistoryRepo(t)
	h.run("alice", h.work, "checkout", "-q", "-b", "offer")
	for _, f := range offerSet {
		h.write(f.Path, string(f.Content))
	}
	h.run("alice", h.work, "add", "-A")
	h.run("alice", h.work, "commit", "-qm", "offer")
	return h
}

func TestChangesExactly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(run func(args ...string), write func(path, content string))
		want    bool
		mayFail bool // go-git cannot diff the tree
	}{
		{name: "exact", want: true},
		{name: "main moved on", want: true, edit: func(run func(...string), write func(string, string)) {
			run("checkout", "-q", "main")
			write("tenant-a/next.yaml", "kind: ConfigMap\n")
			run("add", "-A")
			run("commit", "-qm", "next")
			run("checkout", "-q", "offer")
		}},
		{name: "extra file", edit: func(run func(...string), write func(string, string)) {
			write("tenant-a/evil.yaml", "kind: ClusterRoleBinding\n")
			run("add", "-A")
			run("commit", "-q", "--amend", "--no-edit")
		}},
		{name: "changed file", edit: func(run func(...string), write func(string, string)) {
			write("templates/a.yaml", "kind: Other\n")
			run("add", "-A")
			run("commit", "-q", "--amend", "--no-edit")
		}},
		{name: "missing file", edit: func(run func(...string), _ func(string, string)) {
			run("rm", "-q", "templates/.marker")
			run("commit", "-q", "--amend", "--no-edit")
		}},
		{name: "executable file", edit: func(run func(...string), _ func(string, string)) {
			run("update-index", "--chmod=+x", "templates/a.yaml")
			run("commit", "-q", "--amend", "--no-edit")
		}},
		{name: "unreadable tree", mayFail: true, edit: func(run func(...string), write func(string, string)) {
			write("x\x01/y.yaml", "kind: ClusterRoleBinding\n")
			run("add", "-A")
			run("commit", "-q", "--amend", "--no-edit")
		}},
		{name: "second commit", edit: func(run func(...string), _ func(string, string)) {
			run("commit", "-q", "--allow-empty", "-m", "more")
		}},
		// The tree stays offerSet; a forge rebasing the PR replays the side commit.
		{name: "merge keeping its tree", edit: func(run func(...string), write func(string, string)) {
			run("checkout", "-q", "-b", "side", "main")
			write("tenant-a/evil.yaml", "kind: ClusterRoleBinding\n")
			run("add", "-A")
			run("commit", "-qm", "side")
			run("checkout", "-q", "offer")
			run("merge", "-q", "-s", "ours", "--no-edit", "side")
		}},
		// One commit on main's tip with offerSet as its whole change, but a
		// merge: its second parent rides along.
		{name: "merge on main", edit: func(run func(...string), write func(string, string)) {
			run("checkout", "-q", "-b", "side", "main")
			write("tenant-a/evil.yaml", "kind: ClusterRoleBinding\n")
			run("add", "-A")
			run("commit", "-qm", "side")
			run("checkout", "-q", "-B", "offer", "main")
			run("merge", "-q", "-s", "ours", "--no-commit", "side")
			for _, f := range offerSet {
				write(f.Path, string(f.Content))
			}
			run("add", "-A")
			run("commit", "-qm", "offer")
		}},
		// main and the offer's parent merge the same two commits in opposite
		// orders: two fork points, each with main's old tree.
		{name: "criss-cross", edit: func(run func(...string), write func(string, string)) {
			run("checkout", "-q", "-b", "a", "main")
			run("commit", "-q", "--allow-empty", "-m", "a")
			run("checkout", "-q", "-b", "b", "main")
			run("commit", "-q", "--allow-empty", "-m", "b")
			run("checkout", "-q", "a")
			run("merge", "-q", "--no-ff", "--no-edit", "b")
			run("checkout", "-q", "-B", "offer", "b")
			run("merge", "-q", "--no-ff", "--no-edit", "a~1")
			for _, f := range offerSet {
				write(f.Path, string(f.Content))
			}
			run("add", "-A")
			run("commit", "-qm", "offer")
			run("branch", "-f", "main", "a")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := offerRepo(t)
			if tc.edit != nil {
				tc.edit(func(args ...string) { h.run("alice", h.work, args...) }, h.write)
			}
			h.run("alice", h.work, "push", "-q", "-f", "origin", "main", "offer")
			r, err := Open(h.bare, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.ChangesExactly(revParse(t, h.bare, "main"), revParse(t, h.bare, "offer"), offerSet)
			if err != nil && !tc.mayFail {
				t.Fatalf("ChangesExactly: %v", err)
			}
			if got != tc.want {
				t.Errorf("ChangesExactly = %v, want %v", got, tc.want)
			}
		})
	}
}

// A commit the mirror lacks is ErrNoCommit, which a caller may read again
// once the mirror fetches.
func TestChangesExactlyUnknownCommit(t *testing.T) {
	h := offerRepo(t)
	h.run("alice", h.work, "push", "-q", "origin", "offer")
	r, err := Open(h.bare, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ChangesExactly("0123456789abcdef0123456789abcdef01234567", revParse(t, h.bare, "offer"), offerSet); !errors.Is(err, ErrNoCommit) {
		t.Errorf("err = %v, want ErrNoCommit", err)
	}
}
