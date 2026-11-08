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
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// retryBase and retryCap bound the backoff after a failed build. A failure
// leaves the identity due, but a pass runs only on a change or a new
// connection: on a quiet cluster a failed first build would leave the client
// on its skeleton until something unrelated moved.
const (
	retryBase = time.Second
	retryCap  = 30 * time.Second
)

// buildWorkers bounds the concurrent identity builds in one pass: enough that one
// slow identity doesn't serialize the others, few enough that a pass can't open a
// burst of apiserver calls under every connected token at once.
const buildWorkers = 8

// SessionLive reports whether the session a connection opened under may still
// authenticate (auth.Authenticator.SessionLive). tokenKey is
// restfactory.TokenKey of the connection's token; issued is its Identity's.
type SessionLive func(tokenKey string, issued time.Time) bool

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
	live      SessionLive     // nil: sessions never end
	wake      <-chan struct{} // bus subscription over the inventory kinds (the edge)
	levels    Levels          // the level
	kick      chan struct{}   // a connection was added - rebuild so it gets a first frame
	timeout   time.Duration   // per-identity build bound (buildTimeout; tests shorten it)
	retryBase time.Duration   // first retry delay (retryBase; tests shorten it)
	upgrader  websocket.Upgrader

	mu      sync.Mutex
	conns   map[*conn]struct{}
	retries map[string]*retry // pending rebuild per failed identity key, only while connected

	idents map[string]*frame // last frame per identity key; owned by the Run goroutine
}

// retry is one identity's scheduled rebuild after consecutive failed builds.
type retry struct {
	attempt int
	timer   *time.Timer
}

// frame is the last frame built for one identity and the level it was built at.
type frame struct {
	data  []byte
	deps  []string
	stamp uint64
}

// NewHub builds the hub over a bus subscription (wake) and the levels of the kinds
// that subscription covers. Passing both keeps this package decoupled from the
// specific kind set - the caller (main) owns it. The subscription also carries
// the signal that a session ended, which no level covers: live then closes that
// session's connections without rebuilding any frame.
func NewHub(inventory InventoryFunc, live SessionLive, wake <-chan struct{}, levels Levels, uiOrigin string) *Hub {
	return &Hub{
		inventory: inventory,
		live:      live,
		wake:      wake,
		levels:    levels,
		kick:      make(chan struct{}, 1),
		timeout:   buildTimeout,
		retryBase: retryBase,
		upgrader:  upgrader(uiOrigin),
		conns:     map[*conn]struct{}{},
		retries:   map[string]*retry{},
		idents:    map[string]*frame{},
	}
}

// conn is one WebSocket connection. Within a pass, the one goroutine handling its
// identity (Run or a build worker) is the sole writer of last, the sole
// producer into out (a conflating 1-slot mailbox) and the sole closer of evict;
// passes never overlap. The connection's writePump drains out and writes the
// latest frame, and ends the connection once evict closes. quit is closed on
// teardown.
type conn struct {
	id    auth.Identity
	key   string // groups connections of the same identity (same token -> one build)
	out   chan []byte
	quit  chan struct{}
	evict chan struct{}
	last  []byte // last frame delivered to THIS conn; owned as described above
}

func newConn(id auth.Identity, key string) *conn {
	return &conn{id: id, key: key, out: make(chan []byte, 1), quit: make(chan struct{}), evict: make(chan struct{})}
}

func (h *Hub) add(c *conn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	// Wake Run so the new connection gets its first frame without waiting for a change.
	h.poke()
}

func (h *Hub) remove(c *conn) {
	h.mu.Lock()
	delete(h.conns, c)
	if !h.connectedLocked(c.key) {
		h.settleLocked(c.key)
	}
	h.mu.Unlock()
	close(c.quit) // unblocks any in-flight deliver; the writer stops on its own done
}

// poke wakes Run for a pass outside the change stream.
func (h *Hub) poke() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

// retryLater schedules a pass for key's identity after a failed build, each
// consecutive failure waiting longer up to retryCap. Nothing is scheduled for
// an identity no longer connected.
func (h *Hub) retryLater(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.connectedLocked(key) {
		return
	}
	r := h.retries[key]
	if r == nil {
		r = &retry{}
		h.retries[key] = r
	} else {
		r.timer.Stop()
	}
	d := h.retryDelay(r.attempt)
	if d < retryCap {
		r.attempt++
	}
	r.timer = time.AfterFunc(d, h.poke)
}

func (h *Hub) retryDelay(attempt int) time.Duration {
	return min(h.retryBase<<attempt, retryCap)
}

// settle drops key's pending retry: it built, or it left.
func (h *Hub) settle(key string) {
	h.mu.Lock()
	h.settleLocked(key)
	h.mu.Unlock()
}

func (h *Hub) settleLocked(key string) {
	if r := h.retries[key]; r != nil {
		r.timer.Stop()
		delete(h.retries, key)
	}
}

func (h *Hub) connectedLocked(key string) bool {
	for c := range h.conns {
		if c.key == key {
			return true
		}
	}
	return false
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
// A per-identity build failure is logged and skips that identity, never the
// others; it stays due, and a retry pass is scheduled (retryLater). A rejected
// session ends that identity's connections instead (see reject), and so does an
// ended one, checked per connection before any build: sessions of one token
// share a key, and a logout ends only some of them.
func (h *Hub) reconcile(ctx context.Context) {
	h.mu.Lock()
	byKey := make(map[string][]*conn, len(h.conns))
	var ended []*conn
	for c := range h.conns {
		if h.live != nil && !h.live(c.key, c.id.Issued) {
			ended = append(ended, c)
			continue
		}
		byKey[c.key] = append(byKey[c.key], c)
	}
	h.mu.Unlock()
	h.reject(ended)

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
	failed := make([]bool, len(due))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(buildWorkers, len(due)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				built[i], failed[i] = h.build(ctx, due[i].conns, due[i].prev)
			}
		}()
	}
	for i := range due {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	for i, f := range built {
		key := due[i].key
		switch {
		case f != nil:
			h.idents[key] = f
			h.settle(key)
		case failed[i]:
			delete(h.idents, key)
			h.retryLater(key)
		default:
			delete(h.idents, key)
		}
	}
}

// build builds one identity's frame, delivers it, and returns it stamped with the
// level read before the build, so a move during the build leaves it due. prev is
// the identity's last frame, nil when it has none. Nil on failure (failed is
// set unless the session was rejected), or when the frame's repos differ from
// prev's and anything moved mid-build (no pre-build level was read for its new
// repos; the move itself brings the next pass).
func (h *Hub) build(ctx context.Context, conns []*conn, prev *frame) (f *frame, failed bool) {
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
		// An ended session is one connection's, not the token's: the retry's
		// sweep closes it and builds for the rest.
		if errors.Is(err, auth.ErrRejected) && !errors.Is(err, auth.ErrSessionEnded) {
			h.reject(conns)
			return nil, false
		}
		return nil, true
	}
	data, err := json.Marshal(inv)
	if err != nil {
		return nil, true
	}
	sendLatest(conns, data)
	deps := h.levels.Deps(inv)
	if slices.Equal(deps, prevDeps) {
		return &frame{data: data, deps: deps, stamp: stamp}, false
	}
	// Stamp is read after the build here; Total moves before any key does, so an
	// unchanged Total proves no move landed before that read.
	f = &frame{data: data, deps: deps, stamp: h.levels.Stamp(deps)}
	if h.levels.Total() != total {
		return nil, false
	}
	return f, false
}

// reject ends conns whose session is over - a TokenReview rejected it, or it
// ended (signed out, past its lifetime): a frozen frame would otherwise stand
// in for it. Both verdicts outlive the connection, so the client's reconnect
// meets the 401 that signs it out.
func (h *Hub) reject(conns []*conn) {
	for _, c := range conns {
		select {
		case <-c.evict:
		default:
			close(c.evict)
		}
	}
}

// sendLatest delivers data to each of conns that doesn't already hold it.
// Frames are never written after marshalling, so last shares data, not a copy.
func sendLatest(conns []*conn, data []byte) {
	for _, c := range conns {
		if bytes.Equal(c.last, data) {
			continue // this connection already holds the latest frame
		}
		c.last = data
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
