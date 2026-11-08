// Package ttlcache is a tiny string-keyed, TTL-bounded cache shared by the
// identity layers (per-token Kubernetes clients, TokenReview results, per-token
// visible-namespace sets). All three want the same thing: memoize a value by a
// token hash for a short window so a request burst doesn't re-do expensive work,
// and never grow without bound - writes sweep entries past their TTL at most once
// per TTL, so the map is sized by distinct keys seen within two windows, not the
// process lifetime.
package ttlcache

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Cache memoizes values of type V by string key for ttl. The zero Cache is not
// usable; build one with New. Safe for concurrent use.
type Cache[V any] struct {
	ttl time.Duration

	mu     sync.Mutex
	m      map[string]entry[V]
	swept  time.Time
	flight map[string]*call[V] // Do calls in progress, by key
}

type entry[V any] struct {
	val V
	at  time.Time
}

// New builds a Cache whose entries live for ttl.
func New[V any](ttl time.Duration) *Cache[V] {
	return &Cache[V]{ttl: ttl, m: map[string]entry[V]{}, flight: map[string]*call[V]{}}
}

// call is one Do in progress; done closes once val and err are final.
type call[V any] struct {
	done chan struct{}
	val  V
	err  error
}

// errFillAborted is what waiters see when the fill they waited on never
// returned (it panicked): never a zero value posing as a result.
var errFillAborted = errors.New("ttlcache: fill aborted")

// Do runs fill for key unless a Do for key is already in flight, in which
// case it waits for that call's result instead: concurrent misses on one key
// cost one fill. It neither reads nor writes the cache; fill does. A waiter
// whose ctx ends first returns ctx.Err() and leaves the call running for the
// others, so a fill that serves waiters must not stop with its own caller.
func (c *Cache[V]) Do(ctx context.Context, key string, fill func() (V, error)) (V, error) {
	c.mu.Lock()
	if f, ok := c.flight[key]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.val, f.err
		case <-ctx.Done():
			var zero V
			return zero, ctx.Err()
		}
	}
	f := &call[V]{done: make(chan struct{}), err: errFillAborted}
	c.flight[key] = f
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.flight, key)
		c.mu.Unlock()
		close(f.done)
	}()
	f.val, f.err = fill()
	return f.val, f.err
}

// Get returns the cached value for key if present and unexpired.
func (c *Cache[V]) Get(key string) (V, bool) { return c.GetWithin(key, c.ttl) }

// GetWithin is Get for a caller whose own bound on an entry's age, maxAge, may
// be shorter than the cache's TTL right now.
func (c *Cache[V]) GetWithin(key string, maxAge time.Duration) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok && time.Since(e.at) < min(c.ttl, maxAge) {
		return e.val, true
	}
	var zero V
	return zero, false
}

// Put stores val under key, stamped now, and evicts expired entries so the cache
// stays bounded by the keys seen within two TTL windows. The cache is reachable
// before authentication (it negative-caches rejected tokens), so unbounded growth
// would be a token-spray memory leak; evict-on-write closes that. The sweep runs
// at most once per TTL: per write, a spray would make every write scan the map
// under the lock every request takes.
func (c *Cache[V]) Put(key string, val V) { c.PutAt(key, val, time.Now()) }

// PutAt is Put for a value as of at: one derived from a read that began then
// ages from then, not from when the read returned.
func (c *Cache[V]) PutAt(key string, val V, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if now.Sub(c.swept) >= c.ttl {
		for k, e := range c.m {
			if now.Sub(e.at) >= c.ttl {
				delete(c.m, k)
			}
		}
		c.swept = now
	}
	c.m[key] = entry[V]{val: val, at: at}
}

// Delete drops key.
func (c *Cache[V]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}
