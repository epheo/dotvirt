package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
)

func echoInventory(_ context.Context, id auth.Identity) (model.Inventory, error) {
	// Echo the username as the single project name, so a frame proves whose identity
	// built it.
	return model.Inventory{Projects: []model.Project{{Name: id.Username}}}, nil
}

func testLevels() Levels {
	return RepoLevels(eventbus.New(), eventbus.GitChanged, eventbus.LiveChanged, eventbus.GitChanged)
}

func newTestHub() *Hub {
	return NewHub(echoInventory, make(chan struct{}, 1), testLevels(), "")
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

	// No change -> reconcile again delivers no duplicate for a.
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
// newer frame replaces the pending one - the client converges to latest, never sends
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

// TestReconcileBoundsOneIdentityBuild pins the liveness guarantee: a build that
// never returns on its own (a stalled apiserver call under one user's token) is
// cut off by the per-identity deadline, so the other subscribers still get frames.
func TestReconcileBoundsOneIdentityBuild(t *testing.T) {
	stall := func(ctx context.Context, id auth.Identity) (model.Inventory, error) {
		if id.Username == "stuck" {
			<-ctx.Done()
			return model.Inventory{}, ctx.Err()
		}
		return echoInventory(ctx, id)
	}
	h := NewHub(stall, make(chan struct{}, 1), testLevels(), "")
	h.timeout = 50 * time.Millisecond
	stuck, alice := testConn("stuck"), testConn("alice")
	h.add(stuck)
	h.add(alice)

	done := make(chan struct{})
	go func() { h.reconcile(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile wedged on one identity's build")
	}
	if got := frameProject(t, alice); got != "alice" {
		t.Errorf("alice received %q, want her own frame despite the stalled build", got)
	}
}

// repoHub serves each user one project on repo "https://forge/<user>" and counts
// builds per user.
func repoHub(bus *eventbus.Bus) (*Hub, *sync.Map) {
	var builds sync.Map
	inv := func(_ context.Context, id auth.Identity) (model.Inventory, error) {
		n, _ := builds.LoadOrStore(id.Username, new(atomic.Int64))
		n.(*atomic.Int64).Add(1)
		return model.Inventory{Projects: []model.Project{{Name: id.Username, Repo: "https://forge/" + id.Username + ".git"}}}, nil
	}
	return NewHub(inv, make(chan struct{}, 1), RepoLevels(bus, eventbus.GitChanged, eventbus.LiveChanged, eventbus.GitChanged), ""), &builds
}

func takeBuilds(builds *sync.Map) map[string]int64 {
	out := map[string]int64{}
	builds.Range(func(k, v any) bool {
		if n := v.(*atomic.Int64).Swap(0); n > 0 {
			out[k.(string)] = n
		}
		return true
	})
	return out
}

// TestReconcileRebuildsOnlyMovedIdentities pins the per-repo fan-out: a push to
// one repo rebuilds only the frames showing it, a new tab of a current identity
// gets the cached frame without a build, and an unscoped kind rebuilds everyone.
func TestReconcileRebuildsOnlyMovedIdentities(t *testing.T) {
	bus := eventbus.New()
	h, builds := repoHub(bus)
	alice, bob := testConn("alice"), testConn("bob")
	h.add(alice)
	h.add(bob)
	want := func(step string, n map[string]int64) {
		t.Helper()
		if got := takeBuilds(builds); fmt.Sprint(got) != fmt.Sprint(n) {
			t.Fatalf("%s: builds %v, want %v", step, got, n)
		}
	}

	h.reconcile(context.Background())
	want("first pass", map[string]int64{"alice": 1, "bob": 1})
	h.reconcile(context.Background())
	want("nothing moved", map[string]int64{})

	bus.PublishKey(eventbus.GitChanged, "https://forge/alice")
	h.reconcile(context.Background())
	want("push to alice's repo", map[string]int64{"alice": 1})

	bus.PublishKey(eventbus.GitChanged, "https://forge/nobody")
	h.reconcile(context.Background())
	want("push to an unseen repo", map[string]int64{})

	bob2 := testConn("bob")
	h.add(bob2)
	h.reconcile(context.Background())
	want("new tab", map[string]int64{})
	if got := frameProject(t, bob2); got != "bob" {
		t.Fatalf("bob's new tab got %q, want the cached frame", got)
	}

	bus.Publish(eventbus.LiveChanged)
	h.reconcile(context.Background())
	want("unscoped kind", map[string]int64{"alice": 1, "bob": 1})
}

// TestReconcileLevelMovedMidBuild: a frame whose level moved while it built may
// miss that move, so the identity stays due.
func TestReconcileLevelMovedMidBuild(t *testing.T) {
	bus := eventbus.New()
	var builds atomic.Int64
	inv := func(_ context.Context, id auth.Identity) (model.Inventory, error) {
		if builds.Add(1) == 1 {
			bus.Publish(eventbus.LiveChanged)
		}
		return echoInventory(context.Background(), id)
	}
	h := NewHub(inv, make(chan struct{}, 1), RepoLevels(bus, eventbus.GitChanged, eventbus.LiveChanged, eventbus.GitChanged), "")
	h.add(testConn("a"))
	h.reconcile(context.Background())
	h.reconcile(context.Background())
	h.reconcile(context.Background())
	if n := builds.Load(); n != 2 {
		t.Fatalf("builds = %d, want 2 (one rebuild after the mid-build move)", n)
	}
}

// TestReconcileWorkerPoolBound: builds run concurrently, never more than
// buildWorkers at once, and every identity still gets its own frame.
func TestReconcileWorkerPoolBound(t *testing.T) {
	var active, peak atomic.Int64
	release := make(chan struct{})
	inv := func(ctx context.Context, id auth.Identity) (model.Inventory, error) {
		n := active.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		active.Add(-1)
		return echoInventory(ctx, id)
	}
	h := NewHub(inv, make(chan struct{}, 1), testLevels(), "")
	conns := make([]*conn, 3*buildWorkers)
	for i := range conns {
		conns[i] = testConn(fmt.Sprintf("u%d", i))
		h.add(conns[i])
	}

	done := make(chan struct{})
	go func() { h.reconcile(context.Background()); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for active.Load() < buildWorkers {
		if time.Now().After(deadline) {
			t.Fatalf("only %d concurrent builds, want %d", active.Load(), buildWorkers)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	<-done
	if p := peak.Load(); p != buildWorkers {
		t.Fatalf("peak concurrent builds = %d, want %d", p, buildWorkers)
	}
	for i, c := range conns {
		if got := frameProject(t, c); got != fmt.Sprintf("u%d", i) {
			t.Fatalf("conn %d got %q", i, got)
		}
	}
}
