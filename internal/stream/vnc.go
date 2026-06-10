package stream

import (
	"io"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

// VNCDialer opens a VNC stream to a VMI as a net.Conn carrying the RFB protocol.
// Implemented by the cluster client.
type VNCDialer interface {
	VNCConn(namespace, name string) (net.Conn, error)
}

// VNCProxy bridges a browser noVNC WebSocket to KubeVirt's VNC subresource.
type VNCProxy struct {
	dialer VNCDialer
}

// NewVNCProxy builds a VNC proxy over the given dialer.
func NewVNCProxy(d VNCDialer) *VNCProxy { return &VNCProxy{dialer: d} }

// Handler upgrades the request to a WebSocket and pipes it bidirectionally to the
// VMI's VNC stream: browser binary frames -> virt-api, and back. Path params
// supply namespace/name.
func (p *VNCProxy) Handler(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	conn, err := p.dialer.VNCConn(namespace, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer conn.Close()

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

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
