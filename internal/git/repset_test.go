package git

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epheo/dotvirt/pkg/forge"
)

func TestRepoSetCachesByURL(t *testing.T) {
	bareA := seedRepo(t)
	bareB := seedRepo(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", nil, false, nil, time.Hour) // long interval: poll won't fire in-test

	read1, write1, err := rs.Get(bareA)
	if err != nil {
		t.Fatalf("Get(A): %v", err)
	}
	if read1 == nil || write1 == nil {
		t.Fatal("Get returned nil read/write")
	}

	// Same URL -> same cached instances.
	read2, write2, err := rs.Get(bareA)
	if err != nil {
		t.Fatalf("Get(A) again: %v", err)
	}
	if read1 != read2 || write1 != write2 {
		t.Error("RepoSet did not cache: same URL returned different instances")
	}

	// Different URL -> different instances.
	readB, _, err := rs.Get(bareB)
	if err != nil {
		t.Fatalf("Get(B): %v", err)
	}
	if readB == read1 {
		t.Error("different URLs returned the same read repo")
	}

	// The read view actually works (proves Get opened a usable mirror).
	if _, err := read1.VMManifests("main"); err != nil {
		t.Errorf("cached read repo unusable: %v", err)
	}
}

func TestRepoSetGetBadURLErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", nil, false, nil, time.Hour)

	if _, _, err := rs.Get("/nonexistent/repo.git"); err == nil {
		t.Error("expected an error opening a nonexistent repo")
	}
}

// fakeClock is a RepoSet clock the test advances; reads happen under s.mu in
// poll goroutines, so it is atomic.
type fakeClock struct{ ns atomic.Int64 }

func (c *fakeClock) now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *fakeClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

// A failed Open is served from the negative cache until the backoff passes,
// and a webhook Poke clears it early.
func TestRepoSetBacksOffFailedOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", nil, false, nil, time.Hour)
	clock := &fakeClock{}
	rs.now = clock.now
	var opens atomic.Int32
	rs.open = func(string, string, forge.TokenSource) (*Repo, error) {
		opens.Add(1)
		return nil, errors.New("dead")
	}

	for range 3 {
		if _, _, err := rs.Get("https://forge/o/r.git"); err == nil {
			t.Fatal("expected the open error")
		}
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("opens within backoff = %d, want 1", n)
	}
	clock.advance(openBackoff + time.Second)
	_, _, _ = rs.Get("https://forge/o/r")
	if n := opens.Load(); n != 2 {
		t.Fatalf("opens after backoff = %d, want 2", n)
	}
	rs.Poke("https://forge/o/r")
	_, _, _ = rs.Get("https://forge/o/r")
	if n := opens.Load(); n != 3 {
		t.Fatalf("opens after poke = %d, want 3", n)
	}
}

// Concurrent Gets of one unopened repo share a single clone.
func TestRepoSetSharesConcurrentOpen(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", nil, false, nil, time.Hour)
	release := make(chan struct{})
	var opens atomic.Int32
	rs.open = func(url, user string, tok forge.TokenSource) (*Repo, error) {
		opens.Add(1)
		<-release
		return Open(url, user, tok)
	}

	var wg sync.WaitGroup
	reads := make([]*Repo, 5)
	for i := range reads {
		wg.Go(func() {
			r, _, err := rs.Get(bare)
			if err != nil {
				t.Errorf("Get: %v", err)
			}
			reads[i] = r
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := opens.Load(); n != 1 {
		t.Fatalf("opens = %d, want 1", n)
	}
	for _, r := range reads {
		if r != reads[0] {
			t.Fatal("concurrent Gets returned different repos")
		}
	}
}

func waitEvicted(t *testing.T, rs *RepoSet, url string) {
	t.Helper()
	key := forge.NormalizeRepoURL(url)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rs.mu.Lock()
		_, ok := rs.cache[key]
		rs.mu.Unlock()
		if !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("repo was not evicted")
}

func TestRepoSetEvictsIdleRepo(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", nil, false, nil, 10*time.Millisecond)
	clock := &fakeClock{}
	rs.now = clock.now

	first, _, err := rs.Get(bare)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	clock.advance(idleTimeout + time.Second)
	waitEvicted(t, rs, bare)

	again, _, err := rs.Get(bare)
	if err != nil {
		t.Fatalf("Get after eviction: %v", err)
	}
	if again == first {
		t.Error("Get after eviction must reopen the repo")
	}
}

func TestRepoSetEvictsGoneRepo(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", nil, false, nil, time.Hour)
	if _, _, err := rs.Get(bare); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := os.RemoveAll(bare); err != nil {
		t.Fatal(err)
	}
	rs.Poke(bare)
	waitEvicted(t, rs, bare)
}
