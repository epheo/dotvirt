package stream

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/epheo/dotvirt/internal/auth"
)

type fakeDialer struct {
	dials int
	conn  net.Conn
	err   error
}

func (f *fakeDialer) VNCConn(namespace, name string) (net.Conn, error) {
	f.dials++
	return f.conn, f.err
}

func vncServer(t *testing.T, d *fakeDialer) *httptest.Server {
	t.Helper()
	p := NewVNCProxy(func(string) (VNCDialer, error) { return d, nil }, "")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /vnc/{namespace}/{name}", func(w http.ResponseWriter, r *http.Request) {
		p.Handler(w, r.WithContext(auth.NewContext(r.Context(), auth.Identity{Token: "tok"})))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestVNCRefusesBeforeDial(t *testing.T) {
	d := &fakeDialer{err: errors.New("unreachable")}
	srv := vncServer(t, d)

	resp, err := http.Get(srv.URL + "/vnc/ns/vm")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("plain GET status = %d, want 400", resp.StatusCode)
	}

	hdr := http.Header{"Origin": {"https://evil.example"}}
	_, resp, err = websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/vnc/ns/vm", hdr)
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin: err=%v resp=%v, want 403", err, resp)
	}
	if d.dials != 0 {
		t.Errorf("virt-api dialed %d times for refused requests", d.dials)
	}
}

func TestVNCDialErrorHidesDetail(t *testing.T) {
	d := &fakeDialer{err: errors.New("dial https://10.0.0.1:6443 token=abc")}
	srv := vncServer(t, d)
	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/vnc/ns/vm", nil)
	if err == nil || resp == nil {
		t.Fatalf("want a refused handshake, got err=%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	if body := string(buf[:n]); strings.Contains(body, "10.0.0.1") || strings.Contains(body, "token") {
		t.Errorf("dial error leaked: %q", body)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestVNCReadLimitClosesOversizedFrame(t *testing.T) {
	vmSide, proxySide := net.Pipe()
	defer func() { _ = vmSide.Close() }()
	d := &fakeDialer{conn: proxySide}
	srv := vncServer(t, d)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/vnc/ns/vm", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()
	// Drain the VM side so a small frame would not block the proxy.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := vmSide.Read(buf); err != nil {
				return
			}
		}
	}()
	if err := ws.WriteMessage(websocket.BinaryMessage, make([]byte, vncReadLimit+1)); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("an oversized frame must close the socket")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("socket stayed open after an oversized frame")
	}
}
