package stream

import (
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/restfactory"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
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

// Handler upgrades a request to a WebSocket carrying the caller's inventory. The
// auth middleware has already validated the session and injected the Identity (the
// cookie rides the WS upgrade), so we register the connection under that identity;
// the central reconciler builds its first frame on connect (the add() kick) and the
// freshest frame on every change, under the connection's own identity.
func (h *Hub) Handler(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	wsconn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an error response
	}

	// Group connections by the token's hash (same key the API caches use), so two
	// tabs of one user share a build — and the raw bearer never becomes a map key.
	c := &conn{id: id, key: restfactory.TokenKey(id.Token), out: make(chan []byte, 1), quit: make(chan struct{})}
	h.add(c)
	defer h.remove(c)

	done := make(chan struct{})
	go readPump(wsconn, done) // drains control frames / detects disconnect
	writePump(wsconn, c, done)
}

// readPump consumes incoming messages (we expect only pongs/close) so the
// connection's read deadline is maintained and disconnects are detected.
func readPump(wsconn *websocket.Conn, done chan<- struct{}) {
	defer close(done)
	wsconn.SetReadLimit(512)
	_ = wsconn.SetReadDeadline(time.Now().Add(pongWait))
	wsconn.SetPongHandler(func(string) error {
		return wsconn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		if _, _, err := wsconn.ReadMessage(); err != nil {
			return
		}
	}
}

// writePump is the level-triggered writer: it drains the connection's conflating
// mailbox (always the latest frame the reconciler delivered) and writes it, so a
// slow client converges to current state without dropped frames. The ping keeps the
// TCP connection alive (it is NOT a content heartbeat — content arrives on change).
func writePump(wsconn *websocket.Conn, c *conn, done <-chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = wsconn.Close()
	}()
	for {
		select {
		case <-done:
			return
		case data := <-c.out:
			_ = wsconn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := wsconn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			_ = wsconn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := wsconn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
