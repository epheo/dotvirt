package stream

import (
	"io"
	"net"
	"net/http"

	"github.com/gorilla/websocket"

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
}

// NewVNCProxy builds a VNC proxy that dials as the requesting user.
func NewVNCProxy(d DialerForToken) *VNCProxy { return &VNCProxy{dialerFor: d} }

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
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	dialer, err := p.dialerFor(id.Token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	conn, err := dialer.VNCConn(namespace, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = conn.Close() }()

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = ws.Close() }()

	// Bridge both directions; when either side ends, tear down both.
	errc := make(chan error, 2)
	go func() { errc <- copyWSToConn(ws, conn) }() // browser -> VMI
	go func() { errc <- copyConnToWS(conn, ws) }() // VMI -> browser
	<-errc
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
