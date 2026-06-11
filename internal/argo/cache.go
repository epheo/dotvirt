package argo

import (
	"context"
	"sync"
	"time"
)

// DriftCache memoizes the cluster-wide drift map for a short window. Drift is
// catalog data (which Application manages which VM), identical for every tenant,
// but the inventory is recomputed per subscriber on every change — without this,
// a broadcast to N viewers would issue N identical cluster-wide Application LISTs.
// One real fetch per window serves them all; staleness is bounded by the TTL,
// which is far shorter than how fast drift meaningfully changes.
type DriftCache struct {
	client *Client
	ttl    time.Duration

	mu  sync.Mutex
	at  time.Time
	val map[string]Drift
}

// NewDriftCache wraps the SA argo client with a TTL-cached VMDrift.
func NewDriftCache(saClient *Client, ttl time.Duration) *DriftCache {
	return &DriftCache{client: saClient, ttl: ttl}
}

// Get returns the drift map, refreshing it at most once per TTL. Concurrent
// callers within a window share one result (and one in-flight error).
func (d *DriftCache) Get(ctx context.Context) (map[string]Drift, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.val != nil && time.Since(d.at) < d.ttl {
		return d.val, nil
	}
	val, err := d.client.VMDrift(ctx)
	if err != nil {
		// Keep serving the last good value within the window rather than flapping
		// every VM to NotTracked on a transient list error.
		if d.val != nil && time.Since(d.at) < d.ttl {
			return d.val, nil
		}
		return nil, err
	}
	d.val, d.at = val, time.Now()
	return val, nil
}
