package git

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/epheo/dotvirt/internal/model"
)

// A fetch stalled on the network must not hold the lock reads take.
func TestReadsDoNotWaitOnFetch(t *testing.T) {
	r, err := Open(seedRepo(t), "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	release := make(chan struct{})
	stalled := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case stalled <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-req.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	cfg, err := r.repo.Load().Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Remotes["origin"].URLs = []string{srv.URL + "/o/r.git"}

	go func() { _ = r.refresh() }()
	<-stalled

	done := make(chan error, 1)
	go func() {
		_, err := r.FileOnBranch("main", "web.yaml")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("FileOnBranch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read blocked behind a stalled fetch")
	}
}

// The mirror holds the remote's branches and nothing else: no forge PR refs
// at clone, and a branch deleted on the remote leaves on the next refresh.
func TestMirrorTracksBranchesOnly(t *testing.T) {
	bare := seedRepo(t)
	gitIn := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", bare}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitIn("update-ref", "refs/pull/1/head", "main")
	gitIn("branch", "feature", "main")

	r, err := Open(bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	refs, err := r.repo.Load().References()
	if err != nil {
		t.Fatal(err)
	}
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), "refs/pull/") {
			t.Errorf("clone pulled a forge ref: %s", ref.Name())
		}
		return nil
	})
	if r.branchHash("feature") == "" {
		t.Fatal("clone missed a branch")
	}

	gitIn("branch", "-D", "feature")
	if err := r.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if r.branchHash("feature") != "" {
		t.Error("a branch deleted on the remote survived the refresh")
	}
	if r.branchHash("main") == "" {
		t.Error("refresh lost main")
	}
}

// A long read (a deep history walk, an index rebuild) must not hold up other
// reads of the same mirror, nor the refresh that swaps a new one in.
func TestLongReadDoesNotBlockReads(t *testing.T) {
	r, err := Open(seedRepo(t), "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	inside, release := make(chan struct{}), make(chan struct{})
	walked := make(chan error, 1)
	go func() {
		walked <- r.WalkYAML("main", func(string) bool { return true }, func(string, []byte) error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside
	defer func() {
		close(release)
		if err := <-walked; err != nil {
			t.Errorf("WalkYAML: %v", err)
		}
	}()

	done := make(chan error, 1)
	go func() {
		if r.branchHash("main") == "" {
			done <- errors.New("main did not resolve")
			return
		}
		if _, err := r.FileOnBranch("main", "web.yaml"); err != nil {
			done <- err
			return
		}
		done <- r.refresh()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a read or refresh blocked behind a long read")
	}
}

// A branch deleted on the remote takes its objects with it on the next
// refresh, so a long-lived mirror holds what its branches reach and no more.
func TestRefreshDropsUnreachableObjects(t *testing.T) {
	bare := seedRepo(t)
	r, err := Open(bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	objects := func() int { return len(r.repo.Load().Storer.(*memory.Storage).Objects) }
	seeded := objects()

	w := OpenWrite(bare, "", nil, true)
	if _, err := w.Commit("feature", "add", []model.File{{Path: "alpha/db.yaml", Content: []byte("kind: ConfigMap\n")}}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := r.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if objects() <= seeded {
		t.Fatalf("refresh did not fetch the branch: %d objects", objects())
	}

	if out, err := exec.Command("git", "-C", bare, "branch", "-D", "feature").CombinedOutput(); err != nil {
		t.Fatalf("delete branch: %v\n%s", err, out)
	}
	if err := r.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := objects(); got != seeded {
		t.Errorf("mirror holds %d objects after the branch left, want the %d main reaches", got, seeded)
	}
	if _, err := r.FileOnBranch("main", "web.yaml"); err != nil {
		t.Errorf("main lost content: %v", err)
	}
}
