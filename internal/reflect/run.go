package reflect

import (
	"context"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

// NewIndexer builds the namespace-keyed store every snapshot reflector feeds.
func NewIndexer() cache.Indexer {
	return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
}

// Run starts a reflector feeding store from lw with unstructured objects and
// no resync, and returns immediately. The reflector owns its own
// relist/backoff and stops when ctx is cancelled.
func Run(ctx context.Context, lw cache.ListerWatcher, store cache.Store) {
	RunTyped(ctx, lw, &unstructured.Unstructured{}, store)
}

// RunTyped is Run for a source that yields typed objects; expected is a zero
// value of the watched type, which the reflector uses to drop foreign events.
func RunTyped(ctx context.Context, lw cache.ListerWatcher, expected any, store cache.Store) {
	go cache.NewReflector(lw, expected, store, 0).Run(ctx.Done())
}

// discoveryInterval paces the API probe while a CRD is absent - one lightweight
// discovery GET per tick, ending once the reflector owns a watch connection.
// CRD removal afterwards is not re-probed: the watch goes quiet on a last-known
// state, and uninstalling an operator is outside dotvirt's flow anyway.
const discoveryInterval = time.Minute

// RunWhenServed starts a discovery-gated reflector and returns immediately: it
// re-probes has(gvr) slowly until the API is served, then runs lw (health
// tracked into healthy) into store for the rest of the process. Absence reads
// as an empty store, never a reflector error loop, so a snapshot can watch a
// CRD whose installation is itself something dotvirt proposes.
func RunWhenServed(ctx context.Context, has func(schema.GroupVersionResource) bool, gvr schema.GroupVersionResource, lw *cache.ListWatch, store cache.Store, healthy *atomic.Bool) {
	go runWhenServed(ctx, discoveryInterval, has, gvr, lw, store, healthy)
}

// Served returns has with one side effect: a positive probe also sets flag,
// for a snapshot that reports API presence separately from sync.
func Served(has func(schema.GroupVersionResource) bool, flag *atomic.Bool) func(schema.GroupVersionResource) bool {
	return func(gvr schema.GroupVersionResource) bool {
		ok := has(gvr)
		if ok {
			flag.Store(true)
		}
		return ok
	}
}

func runWhenServed(ctx context.Context, every time.Duration, has func(schema.GroupVersionResource) bool, gvr schema.GroupVersionResource, lw *cache.ListWatch, store cache.Store, healthy *atomic.Bool) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if has(gvr) {
			Run(ctx, TrackHealth(lw, healthy), store)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// AllHealthy ANDs one TrackHealth flag per watch, so a watch re-establishing
// cannot mask another still failing. A flag nobody has wrapped yet (a CRD not
// discovered) must be initialised true: absence is a feature state, not
// staleness.
func AllHealthy(flags []atomic.Bool) bool {
	for i := range flags {
		if !flags[i].Load() {
			return false
		}
	}
	return true
}
