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
	"fmt"
	"log"
	"runtime/debug"
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

// call is one Do in progress: its fill runs in a goroutine of its own, done
// closes once val and err are final, and cancel stops the fill once no caller
// waits on it.
type call[V any] struct {
	done    chan struct{}
	val     V
	err     error
	waiters int // guarded by the cache's mu
	cancel  context.CancelFunc
}

// errFillAborted is what callers see when the fill they waited on never
// returned (it panicked): never a zero value posing as a result.
var errFillAborted = errors.New("ttlcache: fill aborted")

// Do runs fill for key unless a Do for key is already in flight, in which
// case it waits for that call's result instead: concurrent misses on one key
// cost one fill. It neither reads nor writes the cache; fill does. Every
// caller, the one that started the fill included, returns ctx.Err() as soon as
// its own ctx ends, and a caller whose ctx has already ended starts nothing.
// The fill runs under a context that keeps ctx's values and ends only when
// every caller waiting on it has left, so it serves those still waiting and
// stops for nobody.
func (c *Cache[V]) Do(ctx context.Context, key string, fill func(context.Context) (V, error)) (V, error) {
	var zero V
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	c.mu.Lock()
	f, ok := c.flight[key]
	if !ok {
		fctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &call[V]{done: make(chan struct{}), err: errFillAborted, cancel: cancel}
		c.flight[key] = f
		go c.run(fctx, key, f, fill)
	}
	f.waiters++
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.val, f.err
	case <-ctx.Done():
		c.mu.Lock()
		if f.waiters--; f.waiters == 0 {
			// Nobody is left to serve: stop the fill, and let the next caller
			// start a fresh one rather than join one being cancelled.
			f.cancel()
			if c.flight[key] == f {
				delete(c.flight, key)
			}
		}
		c.mu.Unlock()
		return zero, ctx.Err()
	}
}

func (c *Cache[V]) run(ctx context.Context, key string, f *call[V], fill func(context.Context) (V, error)) {
	defer func() {
		if r := recover(); r != nil {
			// The fill runs on its own goroutine, so this is the only trace of
			// where it failed.
			log.Printf("ttlcache: fill panicked: %v\n%s", r, debug.Stack())
			f.err = fmt.Errorf("%w: %v", errFillAborted, r)
		}
		f.cancel()
		c.mu.Lock()
		if c.flight[key] == f {
			delete(c.flight, key)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.val, f.err = fill(ctx)
}

// Get returns the cached value for key if present and unexpired.
func (c *Cache[V]) Get(key string) (V, bool) {
	v, _, ok := c.GetAge(key)
	return v, ok
}

// GetAge is Get that also returns the entry's age, for a caller whose own
// bound on it may be shorter than the cache's TTL.
func (c *Cache[V]) GetAge(key string) (V, time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok {
		if age := time.Since(e.at); age < c.ttl {
			return e.val, age, true
		}
	}
	var zero V
	return zero, 0, false
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
