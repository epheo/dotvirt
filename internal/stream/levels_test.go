package stream

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
)

// A level that moves without changing what a frame shows (the TTL backstop's
// periodic wake) rebuilds the frame and sends nothing.
func TestLevelMoveWithIdenticalFrameSendsNothing(t *testing.T) {
	bus := eventbus.New()
	var builds atomic.Int32
	inv := func(ctx context.Context, id auth.Identity) (model.Inventory, error) {
		builds.Add(1)
		return echoInventory(ctx, id)
	}
	h := NewHub(inv, nil, make(chan struct{}, 1), RepoLevels(bus, eventbus.GitChanged, eventbus.TTLBackstop, eventbus.GitChanged), "")
	a := testConn("a")
	h.add(a)
	h.reconcile(context.Background())
	_ = frameProject(t, a)

	bus.Publish(eventbus.TTLBackstop)
	h.reconcile(context.Background())
	if builds.Load() != 2 {
		t.Fatalf("builds = %d, want a rebuild on the level move", builds.Load())
	}
	select {
	case <-a.out:
		t.Error("an identical frame was resent")
	default:
	}
}
