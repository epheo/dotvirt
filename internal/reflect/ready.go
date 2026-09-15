package reflect

import (
	"context"
	"sync"
	"sync/atomic"
)

// Ready is a one-shot latch for a store's initial relist: Mark it from the
// store's onSynced, poll Done on the hot path, Wait on it at startup. The zero
// value is usable.
type Ready struct {
	done atomic.Bool
	mu   sync.Mutex
	ch   chan struct{}
}

func (r *Ready) c() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ch == nil {
		r.ch = make(chan struct{})
	}
	return r.ch
}

// Mark records the initial relist as landed and releases every Wait.
func (r *Ready) Mark() {
	if r.done.Swap(true) {
		return
	}
	close(r.c())
}

// Done reports whether Mark has been called; lock-free for hot-path reads.
func (r *Ready) Done() bool { return r.done.Load() }

// Wait blocks until Mark or ctx is done, returning ctx.Err() on cancellation.
func (r *Ready) Wait(ctx context.Context) error {
	select {
	case <-r.c():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
