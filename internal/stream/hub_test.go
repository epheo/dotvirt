package stream

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
)

func echoInventory(_ context.Context, id auth.Identity) (model.Inventory, error) {
	// Echo the username as the single project name, so a frame proves whose identity
	// built it.
	return model.Inventory{Projects: []model.Project{{Name: id.Username}}}, nil
}

func newTestHub() *Hub {
	return NewHub(echoInventory, make(chan struct{}, 1), func() uint64 { return 0 })
}

func testConn(name string) *conn {
	return &conn{id: auth.Identity{Username: name}, key: name, out: make(chan []byte, 1), quit: make(chan struct{})}
}

// TestDeliverAfterRemoveDoesNotBlockOrPanic reproduces the teardown race: a frame
// delivered to a connection that was just removed must return quietly (the quit
// channel unblocks deliver), never panic or block. Run with -race for the conn set.
func TestDeliverAfterRemoveDoesNotBlockOrPanic(t *testing.T) {
	h := newTestHub()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		c := testConn("u")
		h.add(c)
		wg.Add(2)
		go func() { defer wg.Done(); h.remove(c) }()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c.deliver([]byte("frame"))
			}
		}()
	}
	wg.Wait()
}

// TestReconcilePerIdentity verifies each connection receives a frame built under its
// OWN identity (the isolation guarantee on the live channel).
func TestReconcilePerIdentity(t *testing.T) {
	h := newTestHub()
	alice, bob := testConn("alice"), testConn("bob")
	h.add(alice)
	h.add(bob)

	h.reconcile(context.Background())

	if got := frameProject(t, alice); got != "alice" {
		t.Errorf("alice received %q, want a tree built under her identity", got)
	}
	if got := frameProject(t, bob); got != "bob" {
		t.Errorf("bob received %q, want a tree built under his identity", got)
	}
}

// TestReconcileDedupAndFreshConn: an unchanged reconcile delivers nothing new (the
// per-connection dedup), but a fresh connection on an already-built identity still
// gets the current frame immediately.
func TestReconcileDedupAndFreshConn(t *testing.T) {
	h := newTestHub()
	a := testConn("a")
	h.add(a)
	h.reconcile(context.Background())
	_ = frameProject(t, a) // drain the first frame

	// No change → reconcile again delivers no duplicate for a.
	h.reconcile(context.Background())
	select {
	case <-a.out:
		t.Error("unchanged reconcile delivered a duplicate frame")
	default:
	}

	// A second tab of the same identity must still get the current frame.
	a2 := testConn("a")
	h.add(a2)
	h.reconcile(context.Background())
	if got := frameProject(t, a2); got != "a" {
		t.Errorf("fresh second connection got %q, want the current frame", got)
	}
}

// TestDeliverConflatesToLatest: when the writer is slow (mailbox already full), a
// newer frame replaces the pending one — the client converges to latest, never sends
// a stale backlog.
func TestDeliverConflatesToLatest(t *testing.T) {
	c := testConn("x")
	c.deliver([]byte("old"))
	c.deliver([]byte("new")) // replaces the pending "old"
	select {
	case data := <-c.out:
		if string(data) != "new" {
			t.Errorf("mailbox held %q, want the latest %q", data, "new")
		}
	default:
		t.Fatal("no frame in mailbox")
	}
	select {
	case <-c.out:
		t.Error("mailbox held more than one (un-conflated) frame")
	default:
	}
}

func frameProject(t *testing.T, c *conn) string {
	t.Helper()
	select {
	case data := <-c.out:
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
