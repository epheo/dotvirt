package reflect

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// eventually polls cond until it holds or the test's patience runs out.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRunWhenServedStartsOnce drives the discovery gate: while has() says no,
// the source is never listed; once it flips, the reflector lists exactly once,
// syncs the store, and the probe loop ends.
func TestRunWhenServedStartsOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var served atomic.Bool
	var probes, lists atomic.Int32
	has := func(schema.GroupVersionResource) bool {
		probes.Add(1)
		return served.Load()
	}
	lw := &cache.ListWatch{
		ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
			lists.Add(1)
			return &unstructured.UnstructuredList{}, nil
		},
		WatchFuncWithContext: func(context.Context, metav1.ListOptions) (watch.Interface, error) {
			return watch.NewFake(), nil
		},
	}
	var healthy atomic.Bool
	var ready Ready
	store := NewStore(NewIndexer(), func() {}, ready.Mark)

	go runWhenServed(ctx, time.Millisecond, has, schema.GroupVersionResource{Resource: "things"}, lw, store, &healthy)

	eventually(t, "a few probes", func() bool { return probes.Load() >= 3 })
	if lists.Load() != 0 {
		t.Fatal("listed before the API was served")
	}
	if healthy.Load() {
		t.Fatal("healthy before the API was served")
	}

	served.Store(true)
	if err := ready.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if !healthy.Load() {
		t.Error("a served, watched source must read healthy")
	}
	after := probes.Load()
	time.Sleep(20 * time.Millisecond)
	if got := probes.Load(); got != after {
		t.Errorf("probing continued after the reflector started: %d -> %d", after, got)
	}
	if got := lists.Load(); got != 1 {
		t.Errorf("reflector started %d times, want 1", got)
	}
}

func TestReadyLatch(t *testing.T) {
	var r Ready
	if r.Done() {
		t.Fatal("zero Ready must not be done")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := r.Wait(ctx); err == nil {
		t.Fatal("Wait must return the context error while unmarked")
	}
	r.Mark()
	r.Mark() // a second relist must not panic on a closed channel
	if !r.Done() {
		t.Fatal("marked Ready must be done")
	}
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
