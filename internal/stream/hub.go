// Package stream pushes live inventory to WebSocket subscribers. A single hub
// listens for change signals (k8s/argo watches + a git poll), recomputes each
// subscriber's OWN inventory (under their identity), and broadcasts it — so the UI
// never polls and one user never receives another tenant's tree.
package stream

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
)

// InventoryFunc computes the inventory visible to one identity (the API server's
// InventoryForIdentity). Each subscriber's frame is built with their own identity,
// so isolation holds on the live channel exactly as it does over HTTP.
type InventoryFunc func(ctx context.Context, id auth.Identity) (model.Inventory, error)

// Hub fans inventory updates out to subscribers. One Hub per process.
type Hub struct {
	inventory InventoryFunc

	mu   sync.Mutex
	subs map[*subscriber]struct{}

	changed <-chan struct{}
}

type subscriber struct {
	identity auth.Identity
	send     chan []byte
	quit     chan struct{} // closed by remove(); senders select on it so send is never closed
	lastJS   string        // last inventory JSON sent, to suppress duplicate frames
}

// push delivers a frame to the subscriber without blocking and without risking a
// send on a closed channel: it races the buffered send against quit (closed when
// the connection is torn down). A full buffer drops the frame — the next tick
// resends current state.
func (s *subscriber) push(data []byte) {
	select {
	case s.send <- data:
	case <-s.quit:
	default:
	}
}

// NewHub builds a Hub over the process-wide change channel — the single bus every
// source (k8s/argo watches, git polls) signals and only the Hub consumes. It must
// be 1-buffered so writers coalesce instead of blocking; the Hub drains bursts and
// recomputes once.
func NewHub(inventory InventoryFunc, changed <-chan struct{}) *Hub {
	return &Hub{
		inventory: inventory,
		subs:      map[*subscriber]struct{}{},
		changed:   changed,
	}
}

// Run drives broadcasts: it waits for change signals (debounced) and also pushes
// on a slow heartbeat so a subscriber that just connected gets current state even
// without an event. Stops when ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	const debounce = 300 * time.Millisecond
	const heartbeat = 15 * time.Second

	timer := time.NewTimer(heartbeat)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.changed:
			time.Sleep(debounce) // coalesce a burst of events into one recompute
			drainSignal(h.changed)
			h.broadcast(ctx)
			resetTimer(timer, heartbeat)
		case <-timer.C:
			h.broadcast(ctx)
			resetTimer(timer, heartbeat)
		}
	}
}

// broadcast recomputes each subscriber's inventory under their identity and pushes
// it, skipping any whose inventory is unchanged. A failing per-user build is
// skipped (transient); the next tick retries.
func (h *Hub) broadcast(ctx context.Context) {
	h.mu.Lock()
	subs := make([]*subscriber, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()

	for _, s := range subs {
		inv, err := h.inventory(ctx, s.identity)
		if err != nil {
			// Transient (token expiry, API blip) — the next tick retries. Log it so a
			// persistent build failure doesn't masquerade as an empty inventory.
			log.Printf("stream: inventory build failed for %s: %v", s.identity.Username, err)
			continue
		}
		data, err := json.Marshal(inv)
		if err != nil {
			continue
		}
		js := string(data)
		if s.lastJS == js {
			continue
		}
		s.lastJS = js
		s.push(data)
	}
}

func drainSignal(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}
