package stream

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
)

// TestPushAfterRemoveDoesNotPanic reproduces the production crash: a sender
// (sendInitial/broadcast) running after the connection was torn down. With the
// quit-channel design, push must return quietly instead of panicking on a closed
// channel. Run with -race to also catch data races on the subscriber set.
func TestPushAfterRemoveDoesNotPanic(t *testing.T) {
	inv := func(ctx context.Context, id auth.Identity) (model.Inventory, error) {
		return model.Inventory{Projects: []model.Project{{Name: id.Username}}}, nil
	}
	h := NewHub(inv, make(chan struct{}, 1))

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		sub := &subscriber{identity: auth.Identity{Username: "u"}, send: make(chan []byte, 1), quit: make(chan struct{})}
		h.add(sub)

		wg.Add(2)
		// One goroutine tears the subscriber down; another keeps pushing. The race
		// between them is exactly the Handler-returns-vs-sendInitial-still-running
		// window that panicked before.
		go func() { defer wg.Done(); h.remove(sub) }()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				sub.push([]byte("frame"))
			}
		}()
	}
	wg.Wait()
}

// TestBroadcastPerIdentity verifies each subscriber receives a frame built under
// its OWN identity (the isolation guarantee on the live channel).
func TestBroadcastPerIdentity(t *testing.T) {
	inv := func(ctx context.Context, id auth.Identity) (model.Inventory, error) {
		// Echo the username as the single project name.
		return model.Inventory{Projects: []model.Project{{Name: id.Username}}}, nil
	}
	h := NewHub(inv, make(chan struct{}, 1))

	alice := &subscriber{identity: auth.Identity{Username: "alice"}, send: make(chan []byte, 1), quit: make(chan struct{})}
	bob := &subscriber{identity: auth.Identity{Username: "bob"}, send: make(chan []byte, 1), quit: make(chan struct{})}
	h.add(alice)
	h.add(bob)

	h.broadcast(context.Background())

	if got := frameProject(t, alice); got != "alice" {
		t.Errorf("alice received %q, want a tree built under her identity", got)
	}
	if got := frameProject(t, bob); got != "bob" {
		t.Errorf("bob received %q, want a tree built under his identity", got)
	}
}

func frameProject(t *testing.T, s *subscriber) string {
	t.Helper()
	select {
	case data := <-s.send:
		var inv model.Inventory
		if err := json.Unmarshal(data, &inv); err != nil {
			t.Fatalf("bad frame: %v", err)
		}
		if len(inv.Projects) == 0 {
			return ""
		}
		return inv.Projects[0].Name
	default:
		t.Fatal("no frame delivered")
		return ""
	}
}
