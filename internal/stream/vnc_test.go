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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

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

func wsURL(srv *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + path
}

// The bridge carries binary frames both ways byte for byte, drops non-binary
// frames instead of writing them into the RFB stream, and closes the browser
// socket when the VM side ends.
func TestVNCBridgesBothWays(t *testing.T) {
	vmSide, proxySide := net.Pipe()
	d := &fakeDialer{conn: proxySide}
	srv := vncServer(t, d)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "/vnc/ns/vm"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()
	_ = vmSide.SetDeadline(time.Now().Add(5 * time.Second))
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))

	// VM -> browser: the RFB greeting arrives as one binary frame.
	go func() { _, _ = vmSide.Write([]byte("RFB 003.008\n")) }()
	mt, data, err := ws.ReadMessage()
	if err != nil || mt != websocket.BinaryMessage || string(data) != "RFB 003.008\n" {
		t.Fatalf("greeting = %d %q %v", mt, data, err)
	}

	// Browser -> VM: the text frame is dropped, the binary one passes.
	if err := ws.WriteMessage(websocket.TextMessage, []byte("noise")); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte{0x03, 0x00}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, err := vmSide.Read(buf)
	if err != nil || string(buf[:n]) != "\x03\x00" {
		t.Fatalf("vm read %q %v, want only the binary frame", buf[:n], err)
	}

	// The VM side closing tears the browser socket down.
	_ = vmSide.Close()
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("socket stayed open after the console closed")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("socket stayed open after the console closed")
	}
}

// The dial runs as the caller: their token picks the dialer, and the
// apiserver's verdict on it maps to a status the UI can act on.
func TestVNCDialAsCaller(t *testing.T) {
	cases := []struct {
		name    string
		dialErr error
		dialer  error
		want    int
	}{
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Resource: "virtualmachineinstances/vnc"}, "vm", errors.New("rbac")), nil, http.StatusForbidden},
		{"not running", apierrors.NewNotFound(schema.GroupResource{Resource: "virtualmachineinstances"}, "vm"), nil, http.StatusNotFound},
		{"no client", nil, errors.New("kubeconfig https://10.0.0.1"), http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotToken, gotNS, gotName string
			d := &recordingDialer{err: c.dialErr, ns: &gotNS, name: &gotName}
			p := NewVNCProxy(func(token string) (VNCDialer, error) {
				gotToken = token
				return d, c.dialer
			}, "")
			mux := http.NewServeMux()
			mux.HandleFunc("GET /vnc/{namespace}/{name}", func(w http.ResponseWriter, r *http.Request) {
				p.Handler(w, r.WithContext(auth.NewContext(r.Context(), auth.Identity{Token: "alice-token"})))
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			_, resp, err := websocket.DefaultDialer.Dial(wsURL(srv, "/vnc/team-a/web"), nil)
			if err == nil || resp == nil {
				t.Fatalf("want a refused handshake, got err=%v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != c.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, c.want)
			}
			if gotToken != "alice-token" {
				t.Errorf("dialer built for %q, want the caller's token", gotToken)
			}
			if c.dialer == nil && (gotNS != "team-a" || gotName != "web") {
				t.Errorf("dialed %s/%s, want team-a/web", gotNS, gotName)
			}
		})
	}
}

func TestVNCWithoutIdentity(t *testing.T) {
	d := &fakeDialer{}
	p := NewVNCProxy(func(string) (VNCDialer, error) { return d, nil }, "")
	rec := httptest.NewRecorder()
	p.Handler(rec, httptest.NewRequest(http.MethodGet, "/vnc/ns/vm", nil))
	if rec.Code != http.StatusUnauthorized || d.dials != 0 {
		t.Errorf("status = %d dials = %d, want 401 and no dial", rec.Code, d.dials)
	}
}

type recordingDialer struct {
	err      error
	ns, name *string
}

func (r *recordingDialer) VNCConn(namespace, name string) (net.Conn, error) {
	*r.ns, *r.name = namespace, name
	return nil, r.err
}
