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
// stale keys, so the map is bounded by keys seen within one window.
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
