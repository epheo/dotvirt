package argo

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestDriftCacheSharesFetch verifies many Get() calls within the TTL trigger only
// one underlying Application list — the whole point (a broadcast to N subscribers
// must not issue N identical cluster-wide LISTs).
func TestDriftCacheSharesFetch(t *testing.T) {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClient(scheme,
		app("openshift-gitops", "managed", []any{
			vmResource("prod", "vm1", "Synced", "Healthy"),
		}),
	)
	var lists int
	dyn.PrependReactor("list", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists++
		return false, nil, nil // fall through to the tracker
	})

	cache := NewDriftCache(&Client{dyn: dyn}, time.Minute)

	for i := 0; i < 5; i++ {
		d, err := cache.Get(context.Background())
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got := d["prod/vm1"]; got.Sync != "Synced" {
			t.Errorf("call %d: drift wrong: %+v", i, got)
		}
	}
	if lists != 1 {
		t.Errorf("expected 1 Application list shared across 5 Gets, got %d", lists)
	}
}

func TestDriftCacheExpires(t *testing.T) {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClient(scheme,
		app("openshift-gitops", "managed", []any{vmResource("prod", "vm1", "Synced", "Healthy")}),
	)
	var lists int
	dyn.PrependReactor("list", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists++
		return false, nil, nil
	})
	cache := NewDriftCache(&Client{dyn: dyn}, time.Nanosecond) // expires immediately

	_, _ = cache.Get(context.Background())
	time.Sleep(time.Millisecond)
	_, _ = cache.Get(context.Background())
	if lists != 2 {
		t.Errorf("expected 2 lists across the TTL boundary, got %d", lists)
	}
}
