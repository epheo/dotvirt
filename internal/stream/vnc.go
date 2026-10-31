package stream

import (
	"io"
	"log"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/epheo/dotvirt/internal/auth"
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
// a per-request, per-user dialer.
type VNCProxy struct {
	dialerFor DialerForToken
	upgrader  websocket.Upgrader
}

// NewVNCProxy builds a VNC proxy that dials as the requesting user; uiOrigin is
// the frontend origin its sockets accept besides same-origin.
func NewVNCProxy(d DialerForToken, uiOrigin string) *VNCProxy {
	return &VNCProxy{dialerFor: d, upgrader: upgrader(uiOrigin)}
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
