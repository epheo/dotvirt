package stream

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// VNCDialer opens a VNC stream to a VMI as a net.Conn carrying the RFB protocol.
// Implemented by the cluster client.
type VNCDialer interface {
	VNCConn(namespace, name string) (net.Conn, error)
}

// DialerForToken returns a VNC dialer authenticated as the given bearer token, so
// the console is opened with the user's own identity (KubeVirt RBAC gates it).
// Supplied by main as a closure over cluster.Factory.For.
type DialerForToken func(token string) (VNCDialer, error)

// VNCProxy bridges a browser noVNC WebSocket to KubeVirt's VNC subresource, using
// a per-request, per-user dialer. An open console outlives any later check of
// its token (virt-api never re-authenticates the stream), so the proxy closes
// it when its session ends.
type VNCProxy struct {
	dialerFor DialerForToken
	upgrader  websocket.Upgrader
	live      SessionLive     // nil: sessions never end
	wake      <-chan struct{} // the session-ended subscription

	mu       sync.Mutex
	consoles map[*console]struct{}
}

// console is one open bridge, tracked under its session.
type console struct {
	key    string
	issued time.Time
	ws     *websocket.Conn
}

// NewVNCProxy builds a VNC proxy that dials as the requesting user. wake fires
// when sessions end, live says which; uiOrigin is the frontend origin its
// sockets accept besides same-origin.
func NewVNCProxy(d DialerForToken, live SessionLive, wake <-chan struct{}, uiOrigin string) *VNCProxy {
	return &VNCProxy{dialerFor: d, upgrader: upgrader(uiOrigin), live: live, wake: wake, consoles: map[*console]struct{}{}}
}

// Run closes, on each wake, the consoles whose session ended. One per proxy.
func (p *VNCProxy) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			p.sweep()
		}
	}
}

func (p *VNCProxy) sweep() {
	if p.live == nil {
		return
	}
	var ended []*console
	p.mu.Lock()
	for c := range p.consoles {
		if !p.live(c.key, c.issued) {
			ended = append(ended, c)
			delete(p.consoles, c)
		}
	}
	p.mu.Unlock()
	for _, c := range ended {
		go endConsole(c.ws) // a stalled console must not hold up the others
	}
}

// track registers c until the returned func runs. A session that ended before
// c was tracked missed the sweep, so it is checked once here.
func (p *VNCProxy) track(c *console) (untrack func()) {
	p.mu.Lock()
	p.consoles[c] = struct{}{}
	p.mu.Unlock()
	if p.live != nil && !p.live(c.key, c.issued) {
		endConsole(c.ws)
	}
	return func() {
		p.mu.Lock()
		delete(p.consoles, c)
		p.mu.Unlock()
	}
}

// closeGrace bounds the close frame to an ended console: a client that stopped
// reading holds the write lock, and its socket closes regardless.
const closeGrace = time.Second

// endConsole closes a console like the hub closes a stream whose session is
// over, so the client reconnects into the 401 that signs it out.
func endConsole(ws *websocket.Conn) {
	_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "session ended"), time.Now().Add(closeGrace))
	_ = ws.Close()
}

// vncReadLimit bounds one browser frame. noVNC sends key, pointer and
// clipboard messages; a larger frame is not a console, it is a memory sink.
const vncReadLimit = 1 << 20

// Handler upgrades the request to a WebSocket and pipes it bidirectionally to the
// VMI's VNC stream: browser binary frames -> virt-api, and back. The dial uses the
// caller's token (Identity from the auth middleware), so a user can only reach a
// console their RBAC permits. Path params supply namespace/name.
func (p *VNCProxy) Handler(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	// Refuse before dialing: a cross-site page or a plain GET must not make
	// virt-api open a console under the caller's token.
	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	if !p.upgrader.CheckOrigin(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	// Dial errors embed apiserver endpoints; the caller gets a fixed message.
	dialer, err := p.dialerFor(id.Token)
	if err != nil {
		log.Printf("vnc: %s/%s: client: %v", namespace, name, err)
		http.Error(w, "cluster access unavailable", http.StatusServiceUnavailable)
		return
	}
	conn, err := dialer.VNCConn(namespace, name)
	if err != nil {
		log.Printf("vnc: %s/%s: dial: %v", namespace, name, err)
		switch {
		case apierrors.IsForbidden(err):
			http.Error(w, "not allowed to open this console", http.StatusForbidden)
		case apierrors.IsNotFound(err):
			http.Error(w, "VM is not running", http.StatusNotFound)
		default:
			http.Error(w, "console unavailable", http.StatusBadGateway)
		}
		return
	}
	defer func() { _ = conn.Close() }()

	ws, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = ws.Close() }()
	ws.SetReadLimit(vncReadLimit)
	defer p.track(&console{key: restfactory.TokenKey(id.Token), issued: id.Issued, ws: ws})()
	if exp := auth.SessionExpiry(id.Issued); !exp.IsZero() {
		lifetime := time.AfterFunc(time.Until(exp), func() { endConsole(ws) })
		defer lifetime.Stop()
	}

	// Bridge both directions; when either side ends, tear down both.
	done := make(chan struct{}, 2)
	go func() { _ = copyWSToConn(ws, conn); done <- struct{}{} }() // browser -> VMI
	go func() { _ = copyConnToWS(conn, ws); done <- struct{}{} }() // VMI -> browser
	<-done
}

// copyWSToConn forwards binary WebSocket frames from the browser to the VNC conn.
func copyWSToConn(ws *websocket.Conn, conn net.Conn) error {
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		if mt != websocket.BinaryMessage {
			continue // noVNC sends binary; ignore anything else
		}
		if _, err := conn.Write(data); err != nil {
			return err
		}
	}
}

// copyConnToWS forwards bytes from the VNC conn to the browser as binary frames.
func copyConnToWS(conn net.Conn, ws *websocket.Conn) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
