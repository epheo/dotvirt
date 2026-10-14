package git

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
