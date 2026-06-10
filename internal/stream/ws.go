package stream

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
	sendBuffer = 8
)

// upgrader accepts same-origin and the configured UI origin. CheckOrigin is
// permissive here because the API already gates CORS; tighten if exposed.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Handler upgrades a request to a WebSocket subscribed to the given branch's
// inventory. It sends the current inventory immediately, then pushes on every
// change until the client disconnects. Branch comes from ?branch=; switching
// branch is a reconnect (the UI closes and reopens).
func (h *Hub) Handler(w http.ResponseWriter, r *http.Request) {
	branch := r.URL.Query().Get("branch")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an error response
	}

	sub := &subscriber{branch: branch, send: make(chan []byte, sendBuffer)}
	h.add(sub)
	defer h.remove(sub)

	// Push current state right away so a fresh connection isn't blank.
	go h.sendInitial(sub)

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
	close(s.send)
}

func (h *Hub) sendInitial(s *subscriber) {
	inv, err := h.inventory(s.branch)
	if err != nil {
		return
	}
	if data, err := json.Marshal(inv); err == nil {
		s.lastJS = string(data)
		select {
		case s.send <- data:
		default:
		}
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
		case data, ok := <-sub.send:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
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
