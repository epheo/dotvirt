package ttlcache

import (
	"strconv"
	"sync"
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
