// Package stream pushes live inventory to WebSocket clients. One central goroutine
// reconciles each connected identity's inventory frame to the latest change-version
// and hands every connection the freshest frame; connections are level-triggered
// writers that always send the latest. There is no debounce (coalescing falls out of
// the build duration — see Run), no heartbeat broadcast (a fresh connection is built
// on connect; a quiet one needs no resend), and no send-buffer overflow (each
// connection's mailbox conflates to the latest frame, so a slow client converges
// instead of dropping frames). The UI never polls, and one user never receives
// another tenant's tree (each frame is built under the connection's identity).
package stream

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
)

// InventoryFunc computes the inventory visible to one identity (the API server's
// InventoryForIdentity). Each frame is built with the connection's own identity, so
// isolation holds on the live channel exactly as it does over HTTP.
type InventoryFunc func(ctx context.Context, id auth.Identity) (model.Inventory, error)

// Hub is the central inventory reconciler. One Hub per process.
type Hub struct {
	inventory InventoryFunc
	wake      <-chan struct{} // bus subscription over the inventory kinds (the edge)
	version   func() uint64   // summed version of those kinds (the level)
	kick      chan struct{}   // a connection was added — rebuild so it gets a first frame

	mu    sync.Mutex
	conns map[*conn]struct{}
}

// NewHub builds the hub over a bus subscription (wake) and a reader for the summed
// version of the kinds that subscription covers. Passing both keeps this package
// decoupled from the specific kind set — the caller (main) owns it.
func NewHub(inventory InventoryFunc, wake <-chan struct{}, version func() uint64) *Hub {
	return &Hub{
		inventory: inventory,
		wake:      wake,
		version:   version,
		kick:      make(chan struct{}, 1),
		conns:     map[*conn]struct{}{},
	}
}

// conn is one WebSocket connection. The Run goroutine is the sole writer of lastJS
// and the sole producer into out (a conflating 1-slot mailbox); the connection's
// writePump drains out and writes the latest frame. quit is closed on teardown.
type conn struct {
	id     auth.Identity
	key    string // groups connections of the same identity (same token → one build)
	out    chan []byte
	quit   chan struct{}
	lastJS string // last frame delivered to THIS conn; owned by the Run goroutine
}

func (h *Hub) add(c *conn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	// Wake Run so the new connection gets its first frame without waiting for a change.
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

func (h *Hub) remove(c *conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
	close(c.quit) // unblocks any in-flight deliver; the writer stops on its own done
}

// Run reconciles every connected identity's frame to the current change-version.
// Self-clocking: on a wake it rebuilds toward the latest version, then re-checks; if
// the version moved while it built (a burst), it loops immediately — the coalescing
// window is the build duration, not a constant, so it batches exactly as much as the
// load demands and adds no latency floor. Each pass emits the freshest frame to every
// connection that hasn't seen it, so clients keep converging under sustained churn
// rather than starving while the builder chases a moving target.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
		case <-h.kick:
		}
		for {
			target := h.version()
			h.reconcile(ctx)
			if h.version() == target {
				break // nothing moved while we built — caught up
			}
		}
	}
}

// reconcile rebuilds the inventory frame once per distinct connected identity (two
// tabs of one user share a build) and delivers it to that identity's connections
// that haven't sent it yet. A per-identity build failure (e.g. token expiry) is
// logged and skips that identity, never the others.
func (h *Hub) reconcile(ctx context.Context) {
	h.mu.Lock()
	byKey := make(map[string][]*conn, len(h.conns))
	for c := range h.conns {
		byKey[c.key] = append(byKey[c.key], c)
	}
	h.mu.Unlock()

	for _, conns := range byKey {
		inv, err := h.inventory(ctx, conns[0].id)
		if err != nil {
			log.Printf("stream: inventory build failed for %s: %v", conns[0].id.Username, err)
			continue
		}
		data, err := json.Marshal(inv)
		if err != nil {
			continue
		}
		js := string(data)
		for _, c := range conns {
			if c.lastJS == js {
				continue // this connection already holds the latest frame
			}
			c.lastJS = js
			c.deliver(data)
		}
	}
}

// deliver puts data in c's conflating 1-slot mailbox, replacing any pending (older)
// frame, so a slow writer always sends the freshest and never builds a backlog.
// Called only from the Run goroutine (single producer), so the drain-then-send is
// race-free; it never blocks (quit unblocks it on teardown).
func (c *conn) deliver(data []byte) {
	select {
	case <-c.out: // drop a stale pending frame
	default:
	}
	select {
	case c.out <- data:
	case <-c.quit:
	}
}
