// Package stream pushes live inventory to WebSocket clients. One central goroutine
// reconciles each connected identity's inventory frame to the latest change-version
// (building the identities that moved on a bounded worker pool) and hands every
// connection the freshest frame; connections are level-triggered writers that
// always send the latest. There is no debounce (coalescing falls out of the build
// duration - see Run), no heartbeat broadcast (a fresh connection gets the current
// frame on connect; a quiet one needs no resend), and no send-buffer overflow (each
// connection's mailbox conflates to the latest frame, so a slow client converges
// instead of dropping frames). The UI never polls, and one user never receives
// another tenant's tree (each frame is built under the connection's identity).
package stream

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
)

// InventoryFunc computes the inventory visible to one identity (the API server's
// InventoryForIdentity). Each frame is built with the connection's own identity, so
// isolation holds on the live channel exactly as it does over HTTP.
type InventoryFunc func(ctx context.Context, id auth.Identity) (model.Inventory, error)

// buildTimeout bounds one identity's frame build. A build can miss the visible-
// namespace cache and call the apiserver under that user's token; unbounded, one
// stalled build would hold its worker and delay that pass's end until TCP gave up.
const buildTimeout = 15 * time.Second

// buildWorkers bounds the concurrent identity builds in one pass: enough that one
// slow identity doesn't serialize the others, few enough that a pass can't open a
// burst of apiserver calls under every connected token at once.
const buildWorkers = 8

// Levels is the change level frames are reconciled against.
type Levels interface {
	// Total moves whenever anything any frame depends on moves: the Run loop's
	// caught-up check.
	Total() uint64
	// Deps names the scoped keys (repos) a built frame depends on.
	Deps(inv model.Inventory) []string
	// Stamp is the level of the unscoped kinds plus the named keys; a frame stays
	// current while its Stamp holds. Equality-only.
	Stamp(deps []string) uint64
}

// Hub is the central inventory reconciler. One Hub per process.
type Hub struct {
	inventory InventoryFunc
	wake      <-chan struct{} // bus subscription over the inventory kinds (the edge)
	levels    Levels          // the level
	kick      chan struct{}   // a connection was added - rebuild so it gets a first frame
	timeout   time.Duration   // per-identity build bound (buildTimeout; tests shorten it)
	upgrader  websocket.Upgrader

	mu    sync.Mutex
	conns map[*conn]struct{}

	idents map[string]*frame // last frame per identity key; owned by the Run goroutine
}

// frame is the last frame built for one identity and the level it was built at.
type frame struct {
	data  []byte
	deps  []string
	stamp uint64
}

// NewHub builds the hub over a bus subscription (wake) and the levels of the kinds
// that subscription covers. Passing both keeps this package decoupled from the
// specific kind set - the caller (main) owns it.
func NewHub(inventory InventoryFunc, wake <-chan struct{}, levels Levels, uiOrigin string) *Hub {
	return &Hub{
		inventory: inventory,
		wake:      wake,
		levels:    levels,
		kick:      make(chan struct{}, 1),
		timeout:   buildTimeout,
		upgrader:  upgrader(uiOrigin),
		conns:     map[*conn]struct{}{},
		idents:    map[string]*frame{},
	}
}

// conn is one WebSocket connection. Within a pass, the one goroutine handling its
// identity (Run or a build worker) is the sole writer of lastJS and the sole
// producer into out (a conflating 1-slot mailbox); passes never overlap. The
// connection's writePump drains out and writes the latest frame. quit is closed on
// teardown.
type conn struct {
	id     auth.Identity
	key    string // groups connections of the same identity (same token -> one build)
	out    chan []byte
	quit   chan struct{}
	lastJS string // last frame delivered to THIS conn; owned as described above
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
// the version moved while it built (a burst), it loops immediately - the coalescing
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
			target := h.levels.Total()
			h.reconcile(ctx)
			if h.levels.Total() == target {
				break // nothing moved while we built - caught up
			}
		}
	}
}

// reconcile rebuilds the frame of every connected identity whose level moved (two
// tabs of one user share a build) on a bounded worker pool, and delivers each frame
// to that identity's connections that haven't sent it yet. An identity is built by
// one worker per pass and passes never overlap, so its frames arrive in build order.
// A per-identity build failure (e.g. token expiry) is logged and skips that
// identity, never the others; it stays due for the next pass.
func (h *Hub) reconcile(ctx context.Context) {
	h.mu.Lock()
	byKey := make(map[string][]*conn, len(h.conns))
	for c := range h.conns {
		byKey[c.key] = append(byKey[c.key], c)
	}
	h.mu.Unlock()

	for key := range h.idents {
		if _, ok := byKey[key]; !ok {
			delete(h.idents, key)
		}
	}

	type job struct {
		key   string
		conns []*conn
		prev  *frame
	}
	var due []job
	for key, conns := range byKey {
		f := h.idents[key]
		if f != nil && h.levels.Stamp(f.deps) == f.stamp {
			sendLatest(conns, f.data) // a new tab of an identity that is current
			continue
		}
		due = append(due, job{key, conns, f})
	}

	built := make([]*frame, len(due))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(buildWorkers, len(due)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				built[i] = h.build(ctx, due[i].conns, due[i].prev)
			}
		}()
	}
	for i := range due {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	for i, f := range built {
		if f == nil {
			delete(h.idents, due[i].key)
			continue
		}
		h.idents[due[i].key] = f
	}
}

// build builds one identity's frame, delivers it, and returns it stamped with the
// level read before the build, so a move during the build leaves it due. prev is
// the identity's last frame, nil when it has none. Nil on failure, or when the
// frame's repos differ from prev's and anything moved mid-build (no pre-build
// level was read for its new repos).
func (h *Hub) build(ctx context.Context, conns []*conn, prev *frame) *frame {
	var prevDeps []string
	if prev != nil {
		prevDeps = prev.deps
	}
	total, stamp := h.levels.Total(), h.levels.Stamp(prevDeps)
	bctx, cancel := context.WithTimeout(ctx, h.timeout)
	inv, err := h.inventory(bctx, conns[0].id)
	cancel()
	if err != nil {
		log.Printf("stream: inventory build failed for %s: %v", conns[0].id.Username, err)
		return nil
	}
	data, err := json.Marshal(inv)
	if err != nil {
		return nil
	}
	sendLatest(conns, data)
	deps := h.levels.Deps(inv)
	if slices.Equal(deps, prevDeps) {
		return &frame{data: data, deps: deps, stamp: stamp}
	}
	// Stamp is read after the build here; Total moves before any key does, so an
	// unchanged Total proves no move landed before that read.
	f := &frame{data: data, deps: deps, stamp: h.levels.Stamp(deps)}
	if h.levels.Total() != total {
		return nil
	}
	return f
}

// sendLatest delivers data to each of conns that doesn't already hold it.
func sendLatest(conns []*conn, data []byte) {
	js := string(data)
	for _, c := range conns {
		if c.lastJS == js {
			continue // this connection already holds the latest frame
		}
		c.lastJS = js
		c.deliver(data)
	}
}

// deliver puts data in c's conflating 1-slot mailbox, replacing any pending (older)
// frame, so a slow writer always sends the freshest and never builds a backlog.
// Called by the one goroutine handling c's identity in a pass (single producer), so
// the drain-then-send is race-free; it never blocks (quit unblocks it on teardown).
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
