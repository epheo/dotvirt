// Package stream pushes live inventory to WebSocket subscribers. A single hub
// listens for change signals (k8s/argo watches + a git poll), recomputes the
// affected branches' inventories, and broadcasts them — so the UI never polls or
// needs a refresh button.
package stream

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/model"
)

// InventoryFunc computes the inventory for a branch (the git provider's method).
type InventoryFunc func(branch string) (model.Inventory, error)

// Hub fans inventory updates out to subscribers. One Hub per process.
type Hub struct {
	inventory InventoryFunc

	mu   sync.Mutex
	subs map[*subscriber]struct{}

	changed chan struct{}
}

type subscriber struct {
	branch string
	send   chan []byte
	lastJS string // last inventory JSON sent, to suppress duplicate frames
}

// NewHub builds a Hub. changed is the shared signal channel that watches and the
// git poll write to; the Hub coalesces bursts and recomputes once.
func NewHub(inventory InventoryFunc) *Hub {
	return &Hub{
		inventory: inventory,
		subs:      map[*subscriber]struct{}{},
		changed:   make(chan struct{}, 1),
	}
}

// Notify signals that some source changed; safe to call from many goroutines.
// Returns the channel writers should send to.
func (h *Hub) Changed() chan<- struct{} { return h.changed }

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
			// Debounce a burst of events into one recompute.
			time.Sleep(debounce)
			drainSignal(h.changed)
			h.broadcast()
			resetTimer(timer, heartbeat)
		case <-timer.C:
			h.broadcast()
			resetTimer(timer, heartbeat)
		}
	}
}

// broadcast recomputes each distinct subscribed branch once and pushes the
// result to its subscribers, skipping any whose inventory is unchanged.
func (h *Hub) broadcast() {
	h.mu.Lock()
	branches := map[string][]*subscriber{}
	for s := range h.subs {
		branches[s.branch] = append(branches[s.branch], s)
	}
	h.mu.Unlock()

	for branch, subs := range branches {
		inv, err := h.inventory(branch)
		if err != nil {
			continue // transient (e.g. fetch failure); next tick retries
		}
		data, err := json.Marshal(inv)
		if err != nil {
			continue
		}
		js := string(data)
		for _, s := range subs {
			if s.lastJS == js {
				continue
			}
			s.lastJS = js
			select {
			case s.send <- data:
			default: // subscriber's buffer full / slow; drop this frame
			}
		}
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
