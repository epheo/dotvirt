package git

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/pkg/forge"
)

func TestRepoSetCachesByURL(t *testing.T) {
	bareA := seedRepo(t)
	bareB := seedRepo(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", "", nil, false, nil, time.Hour) // long interval: poll won't fire in-test

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
	if _, err := read1.FileOnBranch("main", "web.yaml"); err != nil {
		t.Errorf("cached read repo unusable: %v", err)
	}
}

// Peek reads only what is open: it never opens a repo, and finds one by any
// spelling Get opened it under.
func TestRepoSetPeekNeverOpens(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)

	if r, ok := rs.Peek(bare); ok || r != nil {
		t.Fatal("Peek found a repo nothing opened")
	}
	if r, ok := rs.Peek(bare); ok || r != nil {
		t.Fatal("Peek opened the repo")
	}
	read, _, err := rs.Get(bare)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := rs.Peek(bare + "/"); !ok || r != read {
		t.Error("Peek missed the open mirror")
	}
	if r, ok := (*RepoSet)(nil).Peek(bare); ok || r != nil {
		t.Error("a nil RepoSet has nothing open")
	}
}

func TestRepoSetGetBadURLErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)

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
	rs := NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
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
	rs.Created("https://forge/o/r.git")
	_, _, _ = rs.Get("https://forge/o/r")
	if n := opens.Load(); n != 4 {
		t.Fatalf("opens after Created = %d, want 4", n)
	}
}

// A repo whose clone failed comes back without moving a head, so the poll has
// nothing to report: the recovering open itself must publish GitChanged, or
// frames keep showing it unavailable until an unrelated event. A first open
// publishes nothing.
func TestRepoSetPublishesRecoveredOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := eventbus.New()
	rs := NewRepoSet(ctx, "", "", nil, false, bus, time.Hour)
	clock := &fakeClock{}
	rs.now = clock.now
	var fail atomic.Bool
	rs.open = func(string, string, forge.TokenSource) (*Repo, error) {
		if fail.Load() {
			return nil, errors.New("dead")
		}
		return &Repo{}, nil
	}
	ver := func(url string) uint64 { return bus.KeyVersion(eventbus.GitChanged, forge.NormalizeRepoURL(url)) }

	if _, _, err := rs.Get("https://forge/o/fresh.git"); err != nil || ver("https://forge/o/fresh") != 0 {
		t.Fatalf("a first open published: err=%v ver=%d", err, ver("https://forge/o/fresh"))
	}

	fail.Store(true)
	if _, _, err := rs.Get("https://forge/o/r.git"); err == nil {
		t.Fatal("expected the open error")
	}
	fail.Store(false)
	clock.advance(openBackoff + time.Second)
	if _, _, err := rs.Get("https://forge/o/r"); err != nil {
		t.Fatal(err)
	}
	if ver("https://forge/o/r") != 1 {
		t.Fatalf("a recovered open published %d times, want 1", ver("https://forge/o/r"))
	}

	fail.Store(true)
	if _, _, err := rs.Get("https://forge/o/p.git"); err == nil {
		t.Fatal("expected the open error")
	}
	fail.Store(false)
	rs.Poke("https://forge/o/p")
	if _, _, err := rs.Get("https://forge/o/p"); err != nil {
		t.Fatal(err)
	}
	if ver("https://forge/o/p") != 1 {
		t.Fatalf("an open recovered through a poke published %d times, want 1", ver("https://forge/o/p"))
	}
}

// Concurrent Gets of one unopened repo share a single clone.
func TestRepoSetSharesConcurrentOpen(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
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
	rs := NewRepoSet(ctx, "", "", nil, false, nil, 10*time.Millisecond)
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
	rs := NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	if _, _, err := rs.Get(bare); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := os.RemoveAll(bare); err != nil {
		t.Fatal(err)
	}
	rs.Poke(bare)
	waitEvicted(t, rs, bare)
}

// TestRepoSetTokenOnlyToTheForge pins CHG-11 at the one place repos are
// opened: the read clone and the push view of a repo on another host get no
// token, while the forge's own repos keep it.
func TestRepoSetTokenOnlyToTheForge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "https://forge.example", "bot", forge.StaticToken("secret"), false, nil, time.Hour)
	got := map[string]bool{}
	rs.open = func(url, _ string, tok forge.TokenSource) (*Repo, error) {
		got[url] = tok != nil
		return &Repo{creds: creds{url: url, tokenFn: tok}}, nil
	}
	for url, want := range map[string]bool{
		"https://forge.example/dotvirt/team-a.git": true,
		"https://evil.example/dotvirt/team-a.git":  false,
		"http://forge.example/dotvirt/team-b.git":  false,
	} {
		_, write, err := rs.Get(url)
		if err != nil {
			t.Fatalf("Get(%s): %v", url, err)
		}
		if got[url] != want || (write.auth() != nil) != want {
			t.Errorf("%s: read token %v, push token %v; want %v", url, got[url], write.auth() != nil, want)
		}
	}
}
