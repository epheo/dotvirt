package stream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"github.com/epheo/dotvirt/internal/auth"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
	sendBuffer = 8
)

// allowedOrigin is the configured frontend origin (e.g. http://localhost:5173)
// permitted to open WebSockets cross-origin. Empty means same-origin only.
var allowedOrigin string

// SetAllowedOrigin configures the cross-origin policy for WebSocket upgrades. Set
// once at startup from the UI-origin config.
func SetAllowedOrigin(origin string) { allowedOrigin = origin }

// upgrader gates WebSocket origins: WS handshakes are NOT covered by CORS, so an
// unchecked CheckOrigin would let any web page open a socket carrying the victim's
// session cookie and stream their inventory. We accept only same-origin requests
// and the configured UI origin.
var upgrader = websocket.Upgrader{CheckOrigin: checkOrigin}

func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client (no Origin); cookie/Bearer auth still gates the request
	}
	if allowedOrigin != "" && origin == allowedOrigin {
		return true
	}
	// Same-origin: the Origin's host matches the request's Host.
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

// Handler upgrades a request to a WebSocket subscribed to the caller's inventory.
// The auth middleware has already validated the session and injected the Identity
// into the request context (the cookie rides the WS upgrade), so we register the
// subscriber under that identity and push only their tree. It sends current state
// immediately, then pushes on every change until disconnect.
func (h *Hub) Handler(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an error response
	}

	sub := &subscriber{identity: id, send: make(chan []byte, sendBuffer), quit: make(chan struct{})}
	h.add(sub)
	defer h.remove(sub)

	// Push current state right away so a fresh connection isn't blank. NOTE: use a
	// fresh context, not r.Context() — net/http cancels the request context the
	// moment Upgrade hijacks the connection, which would abort the inventory build.
	go h.sendInitial(context.Background(), sub)

	done := make(chan struct{})
	go readPump(conn, done) // drains control frames / detects disconnect
	writePump(conn, sub, done)
}

func (h *Hub) add(s *subscriber) {
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) remove(s *subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
	// Close quit (not send): senders (broadcast, sendInitial) may still be running
	// in other goroutines, and closing send under them would panic. They select on
	// quit and stop. send is intentionally never closed.
	close(s.quit)
}

// sendInitial pushes current state immediately so a fresh connection isn't blank.
// It deliberately does NOT set lastJS — only broadcast (the single Run goroutine)
// owns that field, avoiding a race; the first broadcast re-sends one identical
// frame, which the client renders idempotently.
func (h *Hub) sendInitial(ctx context.Context, s *subscriber) {
	inv, err := h.inventory(ctx, s.identity)
	if err != nil {
		return
	}
	if data, err := json.Marshal(inv); err == nil {
		s.push(data)
	}
}

// readPump consumes incoming messages (we expect only pongs/close) so the
// connection's read deadline is maintained and disconnects are detected.
func readPump(conn *websocket.Conn, done chan<- struct{}) {
	defer close(done)
	conn.SetReadLimit(512)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func writePump(conn *websocket.Conn, sub *subscriber, done <-chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		conn.Close()
	}()
	for {
		select {
		case <-done:
			return
		case data := <-sub.send:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
