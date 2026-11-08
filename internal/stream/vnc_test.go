package stream

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/restfactory"
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
	p := NewVNCProxy(func(string) (VNCDialer, error) { return d, nil }, nil, nil, "")
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
			}, nil, nil, "")
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
	p := NewVNCProxy(func(string) (VNCDialer, error) { return d, nil }, nil, nil, "")
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

// openConsole serves one console for id over p and returns the browser side.
func openConsole(t *testing.T, p *VNCProxy, id auth.Identity) *websocket.Conn {
	t.Helper()
	vmSide, proxySide := net.Pipe()
	t.Cleanup(func() { _ = vmSide.Close() })
	go func() { _, _ = io.Copy(io.Discard, vmSide) }()
	p.dialerFor = func(string) (VNCDialer, error) { return &fakeDialer{conn: proxySide}, nil }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /vnc/{namespace}/{name}", func(w http.ResponseWriter, r *http.Request) {
		p.Handler(w, r.WithContext(auth.NewContext(r.Context(), id)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "/vnc/ns/vm"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	return ws
}

func wantSessionClose(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	_, _, err := ws.ReadMessage()
	if ce := (*websocket.CloseError)(nil); !errors.As(err, &ce) || ce.Code != websocket.ClosePolicyViolation {
		t.Fatalf("console ended with %v, want the session close", err)
	}
}

// A console closes when its session ends and the session-ended signal fires;
// a console of a live session stays open.
func TestVNCClosesEndedSession(t *testing.T) {
	var ended atomic.Bool
	live := func(key string, issued time.Time) bool { return !ended.Load() || key != restfactory.TokenKey("alice") }
	wake := make(chan struct{}, 1)
	p := NewVNCProxy(nil, live, wake, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	alice := openConsole(t, p, auth.Identity{Token: "alice", Issued: time.Now()})
	bob := openConsole(t, p, auth.Identity{Token: "bob", Issued: time.Now()})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		n := len(p.consoles)
		p.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d consoles tracked, want 2", n)
		}
	}

	ended.Store(true)
	wake <- struct{}{}
	wantSessionClose(t, alice)
	_ = bob.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := bob.ReadMessage(); err == nil {
		t.Fatal("unexpected frame")
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("a live session's console ended: %v", err)
	}
}

// A console ends at its session's lifetime, with no signal at all.
func TestVNCClosesAtSessionLifetime(t *testing.T) {
	now := time.Now()
	maxAge := auth.SessionExpiry(now).Sub(now)
	p := NewVNCProxy(nil, nil, nil, "")
	ws := openConsole(t, p, auth.Identity{Token: "alice", Issued: now.Add(100*time.Millisecond - maxAge)})
	wantSessionClose(t, ws)
}

// A console whose session ended before it was tracked missed the sweep, so it
// closes at registration with no signal at all.
func TestVNCClosesSessionEndedBeforeTracking(t *testing.T) {
	p := NewVNCProxy(nil, func(string, time.Time) bool { return false }, nil, "")
	wantSessionClose(t, openConsole(t, p, auth.Identity{Token: "alice", Issued: time.Now()}))
}

// floodDialer's VM side writes as fast as the proxy reads, so a browser that
// stops reading stalls the proxy's writer on the socket.
type floodDialer struct{}

func (floodDialer) VNCConn(string, string) (net.Conn, error) {
	vmSide, proxySide := net.Pipe()
	go func() {
		buf := make([]byte, 32*1024)
		for {
			if _, err := vmSide.Write(buf); err != nil {
				return
			}
		}
	}()
	return proxySide, nil
}

// One ended console whose client stopped reading does not hold up the close
// of another's.
func TestVNCStalledConsoleDoesNotDelaySweep(t *testing.T) {
	var stalledOut, victimOut atomic.Bool
	live := func(key string, _ time.Time) bool {
		switch key {
		case restfactory.TokenKey("stalled"):
			return !stalledOut.Load()
		case restfactory.TokenKey("victim"):
			return !victimOut.Load()
		}
		return true
	}
	wake := make(chan struct{}, 1)
	p := NewVNCProxy(func(string) (VNCDialer, error) { return floodDialer{}, nil }, live, wake, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /vnc/{who}", func(w http.ResponseWriter, r *http.Request) {
		p.Handler(w, r.WithContext(auth.NewContext(r.Context(), auth.Identity{Token: r.PathValue("who"), Issued: time.Now()})))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	stalled, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "/vnc/stalled"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stalled.Close() }() // never read
	victim, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "/vnc/victim"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = victim.Close() }()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := victim.ReadMessage(); err != nil {
				return
			}
		}
	}()
	time.Sleep(500 * time.Millisecond) // the stalled socket's buffers fill
	stalledOut.Store(true)
	wake <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	victimOut.Store(true)
	start := time.Now()
	wake <- struct{}{}
	select {
	case <-closed:
		if d := time.Since(start); d > closeGrace/2 {
			t.Errorf("victim closed %v after its session ended, want within %v", d, closeGrace/2)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("victim console never closed")
	}
}
