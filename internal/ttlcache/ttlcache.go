// Package ttlcache is a tiny string-keyed, TTL-bounded cache shared by the
// identity layers (per-token Kubernetes clients, TokenReview results, per-token
// visible-namespace sets). All three want the same thing: memoize a value by a
// token hash for a short window so a request burst doesn't re-do expensive work,
// and never grow without bound - writes sweep entries past their TTL at most once
// per TTL, so the map is sized by distinct keys seen within two windows, not the
// process lifetime.
package ttlcache

import (
	"sync"
	"time"
)

// Cache memoizes values of type V by string key for ttl. The zero Cache is not
// usable; build one with New. Safe for concurrent use.
type Cache[V any] struct {
	ttl time.Duration

	mu    sync.Mutex
	m     map[string]entry[V]
	swept time.Time
}

type entry[V any] struct {
	val V
	at  time.Time
}

// New builds a Cache whose entries live for ttl.
func New[V any](ttl time.Duration) *Cache[V] {
	return &Cache[V]{ttl: ttl, m: map[string]entry[V]{}}
}

// Get returns the cached value for key if present and unexpired.
func (c *Cache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok && time.Since(e.at) < c.ttl {
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
func (c *Cache[V]) Put(key string, val V) {
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
	c.m[key] = entry[V]{val: val, at: now}
}
