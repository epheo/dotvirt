package ttlcache

import (
	"context"
	"errors"
	"math/rand/v2"
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
// a caller that gives up leaves the call to the rest; one whose context has
// already ended starts nothing.
func TestDoSharesOneFill(t *testing.T) {
	c := New[int](time.Minute)
	var fills atomic.Int32
	release := make(chan struct{})
	fill := func(context.Context) (int, error) {
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
	waitFor(t, "a fill", func() bool { return fills.Load() > 0 })
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Do(gone, "k", fill); !errors.Is(err, context.Canceled) {
		t.Errorf("a caller whose context ended got %v, want context.Canceled", err)
	}
	if _, err := c.Do(gone, "fresh", fill); !errors.Is(err, context.Canceled) {
		t.Errorf("an ended context started a fill: %v", err)
	}
	if v, err := c.Do(context.Background(), "other", func(context.Context) (int, error) { return 1, nil }); err != nil || v != 1 {
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

// The caller that starts a fill leaves on its own deadline like any other;
// once no caller waits, the fill's context ends, and the next caller starts a
// fresh fill instead of joining the cancelled one.
func TestDoCallersLeaveOnTheirOwnDeadline(t *testing.T) {
	c := New[int](time.Minute)
	stopped := make(chan struct{})
	slow := func(ctx context.Context) (int, error) {
		<-ctx.Done()
		close(stopped)
		return 0, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if _, err := c.Do(ctx, "k", slow); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the leader got %v, want its own deadline", err)
	}
	if waited := time.Since(begin); waited > time.Second {
		t.Fatalf("the leader waited %s past its 20ms deadline", waited)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the fill ran on with no caller waiting")
	}
	if v, err := c.Do(context.Background(), "k", func(context.Context) (int, error) { return 3, nil }); err != nil || v != 3 {
		t.Fatalf("the next caller got %d, %v; want a fresh fill", v, err)
	}
}

// A fill that panics hands its callers an error, never a zero value posing as
// a result, and never takes the process down.
func TestDoAbortedFill(t *testing.T) {
	c := New[int](time.Minute)
	_, err := c.Do(context.Background(), "k", func(context.Context) (int, error) { panic("boom") })
	if !errors.Is(err, errFillAborted) {
		t.Errorf("a caller of a panicked fill got %v, want errFillAborted", err)
	}
}

// An entry reports its age from the time it was stored as of, and expires
// with the cache's TTL whatever the caller's own bound.
func TestGetAgeAndPutAt(t *testing.T) {
	c := New[int](time.Hour)
	c.PutAt("old", 1, time.Now().Add(-time.Minute))
	if v, age, ok := c.GetAge("old"); !ok || v != 1 || age < time.Minute || age > 2*time.Minute {
		t.Errorf("a minute-old entry: %d, %s, %v", v, age, ok)
	}
	c.PutAt("ancient", 3, time.Now().Add(-2*time.Hour))
	if _, _, ok := c.GetAge("ancient"); ok {
		t.Error("an entry past the TTL served")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// Callers join and leave at random; each gets its own key's value or its own
// deadline, never hangs, never sees another caller's cancellation, and no fill
// outlives the storm.
func TestDoStorm(t *testing.T) {
	c := New[string](time.Minute)
	var running atomic.Int64
	var wg sync.WaitGroup
	for i := range 4000 {
		wg.Go(func() {
			key := []string{"a", "b", "c"}[i%3]
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(rand.IntN(400))*time.Microsecond)
			defer cancel()
			v, err := c.Do(ctx, key, func(fctx context.Context) (string, error) {
				running.Add(1)
				defer running.Add(-1)
				select {
				case <-time.After(time.Duration(rand.IntN(300)) * time.Microsecond):
					return key, nil
				case <-fctx.Done():
					return "", fctx.Err()
				}
			})
			switch {
			case err == nil && v != key:
				t.Errorf("a caller for %s got %q", key, v)
			case err != nil && (!errors.Is(err, context.DeadlineExceeded) || ctx.Err() == nil):
				t.Errorf("a caller for %s got %v with its context at %v", key, err, ctx.Err())
			}
		})
	}
	wg.Wait()
	waitFor(t, "every fill to end", func() bool { return running.Load() == 0 })
	waitFor(t, "the flight map to drain", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.flight) == 0
	})
}

// A caller still waiting never receives the cancellation of a fill whose
// other caller left.
func TestDoLiveCallerNeverSeesCancel(t *testing.T) {
	c := New[int](time.Minute)
	for range 2000 {
		gate := make(chan struct{})
		fill := func(fctx context.Context) (int, error) {
			select {
			case <-gate:
				return 1, nil
			case <-fctx.Done():
				return 0, fctx.Err()
			}
		}
		short, cancel := context.WithCancel(context.Background())
		go func() { _, _ = c.Do(short, "k", fill) }()
		res := make(chan error, 1)
		go func() {
			_, err := c.Do(context.Background(), "k", fill)
			res <- err
		}()
		cancel()
		close(gate)
		if err := <-res; err != nil {
			t.Fatalf("a live caller got %v", err)
		}
	}
}
