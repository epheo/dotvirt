package auth

import (
	"context"
	"errors"
	"sync"
	"time"
)

// probeTTL is how long a probe outcome other than success answers for the
// next caller. A probe is claimed under a lock before it starts, so callers
// within the window never fan out into outbound requests.
const probeTTL = 10 * time.Second

// probeTimeout bounds every outbound call the sign-in flows make.
const probeTimeout = 15 * time.Second

// errDiscoveryPending answers a caller that will not wait while another runs
// the first discovery.
var errDiscoveryPending = errors.New("discovery: in progress")

// discovery caches a sign-in flow's endpoint document: fetched once, then kept
// for good; a failure answers callers for probeTTL. One fetch runs at a time,
// and its lock is never held across the fetch: the routes that discover are
// reachable before authentication.
type discovery[T any] struct {
	mu   sync.Mutex
	val  *T
	err  error
	at   time.Time
	done chan struct{} // open while a fetch runs
}

// get returns the document, fetching it when none is cached. A caller arriving
// while a fetch runs waits for its outcome when wait is set (a sign-in needs
// the endpoints; the fetch is bounded by probeTimeout), else gets
// errDiscoveryPending.
func (d *discovery[T]) get(ctx context.Context, wait bool, fetch func(context.Context) (*T, error)) (*T, error) {
	d.mu.Lock()
	switch {
	case d.val != nil:
		v := d.val
		d.mu.Unlock()
		return v, nil
	case d.done != nil:
		done := d.done
		d.mu.Unlock()
		if !wait {
			return nil, errDiscoveryPending
		}
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.val, d.err
	case time.Since(d.at) < probeTTL:
		err := d.err
		d.mu.Unlock()
		return nil, err
	}
	done := make(chan struct{})
	d.done = done
	d.mu.Unlock()

	// Detached from the caller: a client hanging up must not stamp a failure.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeTimeout)
	defer cancel()
	v, err := fetch(fctx)

	d.mu.Lock()
	d.val, d.err, d.at, d.done = v, err, time.Now(), nil
	d.mu.Unlock()
	close(done)
	return v, err
}
