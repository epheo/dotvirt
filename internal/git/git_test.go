package git

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
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
	cfg, err := r.repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Remotes["origin"].URLs = []string{srv.URL + "/o/r.git"}

	go func() { _ = r.refresh() }()
	<-stalled

	done := make(chan error, 1)
	go func() {
		_, err := r.VMManifests("main")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("VMManifests: %v", err)
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
	refs, err := r.repo.References()
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
