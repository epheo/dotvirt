package ttlcache

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetPut(t *testing.T) {
	c := New[int](time.Minute)
	if _, ok := c.Get("a"); ok {
		t.Error("empty cache returned a hit")
	}
	c.Put("a", 42)
	if v, ok := c.Get("a"); !ok || v != 42 {
		t.Errorf("Get after Put = (%d, %v), want (42, true)", v, ok)
	}
}

func TestExpiry(t *testing.T) {
	c := New[int](10 * time.Millisecond)
	c.Put("a", 1)
	time.Sleep(20 * time.Millisecond)
	if _, ok := c.Get("a"); ok {
		t.Error("expired entry still returned a hit")
	}
}

// TestEvictOnWrite is the memory-safety property: a write past the TTL drops
// stale keys, so the map is bounded by keys seen within two windows.
func TestEvictOnWrite(t *testing.T) {
	c := New[int](10 * time.Millisecond)
	for i := 0; i < 100; i++ {
		c.Put(strconv.Itoa(i), i)
	}
	time.Sleep(20 * time.Millisecond)
	c.Put("fresh", 0) // triggers eviction of the 100 expired entries
	c.mu.Lock()
	n := len(c.m)
	c.mu.Unlock()
	if n != 1 {
		t.Errorf("after evict-on-write, cache holds %d entries, want 1", n)
	}
}

// A write burst sweeps once per window, not once per write: each sweep scans the
// whole map under the lock. An expired entry it skipped still never serves.
func TestSweepOncePerWindow(t *testing.T) {
	c := New[int](time.Minute)
	c.Put("a", 1)
	c.mu.Lock()
	c.m["a"] = entry[int]{val: 1, at: time.Now().Add(-2 * time.Minute)}
	c.mu.Unlock()
	c.Put("b", 2)
	c.mu.Lock()
	_, kept := c.m["a"]
	c.mu.Unlock()
	if !kept {
		t.Fatal("a write within the window swept the map again")
	}
	if _, ok := c.Get("a"); ok {
		t.Error("an expired entry served a hit")
	}
}

func TestConcurrent(t *testing.T) {
	c := New[int](time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			k := strconv.Itoa(n % 8)
			c.Put(k, n)
			c.Get(k)
		}(i)
	}
	wg.Wait()
}

// BenchmarkPutWarm is one write into a cache holding 10000 live keys: a token
// spray, or one visible-set entry per connected user.
func BenchmarkPutWarm(b *testing.B) {
	c := New[int](time.Minute)
	for i := range 10000 {
		c.Put(strconv.Itoa(i), i)
	}
	b.ReportAllocs()
	for b.Loop() {
		c.Put("hot", 1)
	}
}

// Concurrent misses on one key share one fill; another key fills on its own;
// a waiter that gives up leaves the call to the rest.
func TestDoSharesOneFill(t *testing.T) {
	c := New[int](time.Minute)
	var fills atomic.Int32
	release := make(chan struct{})
	fill := func() (int, error) {
		fills.Add(1)
		<-release
		return 7, nil
	}
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for range 8 {
		wg.Go(func() {
			v, err := c.Do(context.Background(), "k", fill)
			if err != nil {
				t.Error(err)
			}
			results <- v
		})
	}
	for deadline := time.Now().Add(5 * time.Second); fills.Load() == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no fill started")
		}
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Do(gone, "k", fill); !errors.Is(err, context.Canceled) {
		t.Errorf("a waiter whose context ended got %v, want context.Canceled", err)
	}
	if v, err := c.Do(context.Background(), "other", func() (int, error) { return 1, nil }); err != nil || v != 1 {
		t.Errorf("another key: %d, %v", v, err)
	}
	time.Sleep(50 * time.Millisecond) // let every caller join the call in flight
	close(release)
	wg.Wait()
	close(results)
	for v := range results {
		if v != 7 {
			t.Errorf("a caller got %d, want the shared 7", v)
		}
	}
	if n := fills.Load(); n != 1 {
		t.Errorf("%d fills for one key, want 1", n)
	}
}

// A fill that never returns hands its waiters an error, never a zero value
// posing as a result.
func TestDoAbortedFill(t *testing.T) {
	c := New[int](time.Minute)
	started, release := make(chan struct{}), make(chan struct{})
	go func() {
		defer func() { _ = recover() }()
		_, _ = c.Do(context.Background(), "k", func() (int, error) {
			close(started)
			<-release
			panic("boom")
		})
	}()
	<-started
	done := make(chan error)
	go func() {
		_, err := c.Do(context.Background(), "k", func() (int, error) { return 1, nil })
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the waiter join
	close(release)
	if err := <-done; !errors.Is(err, errFillAborted) {
		t.Errorf("a waiter on a panicked fill got %v, want errFillAborted", err)
	}
}
