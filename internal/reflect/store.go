// Package reflect holds the generic reflector plumbing shared by dotvirt's
// in-memory snapshots (clusterstate's live VM/VMI/namespace snapshot, argo's
// drift snapshot, desched's DRS snapshot). The pieces that are genuinely
// reusable — and subtle enough to be worth defining once — are the store
// wrapper that turns a stream of watch deltas into a single coalesced
// "something moved" signal and marks the initial relist complete, and the
// ListWatch wrapper that turns watch errors into a health signal. Each
// snapshot composes these and adds its own typed read methods.
package reflect

import (
	"context"
	"sync/atomic"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// TrackHealth wraps lw so a failed List or Watch flips healthy false and a
// successful Watch establish flips it true — a deterministic, error-driven
// staleness signal, not a TTL. The reflector re-lists+re-watches on a drop, so
// a transient blip that immediately recovers stays healthy; a sustained outage
// (repeated errors) reads as unhealthy. Callers surface it as a "may be stale"
// warning while the store keeps serving its last-good contents.
func TrackHealth(lw *cache.ListWatch, healthy *atomic.Bool) *cache.ListWatch {
	// Sources set the WithContext pair (the reflector cancels in-flight calls on
	// shutdown through it); the deprecated context-free fields stay nil.
	list, watchFn := lw.ListWithContextFunc, lw.WatchFuncWithContext
	return &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			obj, err := list(ctx, o)
			if err != nil {
				healthy.Store(false)
			}
			return obj, err
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			w, err := watchFn(ctx, o)
			healthy.Store(err == nil)
			return w, err
		},
	}
}

// countingStore wraps a cache.Indexer and fires onChange after any mutation a
// reflector applies (Add/Update/Delete/Replace), so the read methods never have to
// diff anything. Replace fires onChange once for a whole relist rather than per
// item — exactly the coarse signal we want — and also fires onSynced the first
// time (the initial LIST landed), for readiness gating.
type countingStore struct {
	cache.Indexer
	onChange func()
	onSynced func() // optional; called once on the first Replace
	didSync  bool
}

// NewStore wraps idx so onChange fires after every reflector mutation and onSynced
// fires exactly once when the initial relist (first Replace) lands. onSynced may be
// nil for a signal-only reflector whose readiness nobody waits on. The result is a
// cache.Indexer (reads delegate to idx) usable as a cache.NewReflector store.
func NewStore(idx cache.Indexer, onChange, onSynced func()) cache.Indexer {
	return &countingStore{Indexer: idx, onChange: onChange, onSynced: onSynced}
}

func (c *countingStore) Add(obj any) error {
	if err := c.Indexer.Add(obj); err != nil {
		return err
	}
	c.onChange()
	return nil
}

func (c *countingStore) Update(obj any) error {
	if err := c.Indexer.Update(obj); err != nil {
		return err
	}
	c.onChange()
	return nil
}

func (c *countingStore) Delete(obj any) error {
	if err := c.Indexer.Delete(obj); err != nil {
		return err
	}
	c.onChange()
	return nil
}

func (c *countingStore) Replace(list []any, rv string) error {
	if err := c.Indexer.Replace(list, rv); err != nil {
		return err
	}
	if !c.didSync {
		c.didSync = true
		if c.onSynced != nil {
			c.onSynced()
		}
	}
	c.onChange()
	return nil
}

// signalStore is a cache.Store that retains NOTHING — it only fires onChange on each
// reflector mutation and onSynced once on the first Replace. For a watch kept purely
// as a change signal (e.g. RoleBindings, watched only to invalidate the per-token
// visibility cache and never read), this avoids holding every object cluster-wide in
// memory. The reflector never reads the store back, so discarding is safe.
type signalStore struct {
	onChange func()
	onSynced func() // optional; called once on the first Replace
	didSync  bool
}

// NewSignalStore builds a retain-nothing cache.Store that fires onChange on every
// reflector mutation and onSynced once on the initial relist.
func NewSignalStore(onChange, onSynced func()) cache.Store {
	return &signalStore{onChange: onChange, onSynced: onSynced}
}

func (s *signalStore) Add(any) error    { s.onChange(); return nil }
func (s *signalStore) Update(any) error { s.onChange(); return nil }
func (s *signalStore) Delete(any) error { s.onChange(); return nil }

func (s *signalStore) Replace(_ []any, _ string) error {
	if !s.didSync {
		s.didSync = true
		if s.onSynced != nil {
			s.onSynced()
		}
	}
	s.onChange()
	return nil
}

// The read half is unused (the reflector never reads its store back) — satisfy
// cache.Store with empty results.
func (s *signalStore) List() []any                        { return nil }
func (s *signalStore) ListKeys() []string                 { return nil }
func (s *signalStore) Get(any) (any, bool, error)         { return nil, false, nil }
func (s *signalStore) GetByKey(string) (any, bool, error) { return nil, false, nil }
func (s *signalStore) Resync() error                      { return nil }
