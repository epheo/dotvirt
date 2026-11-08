package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/restfactory"
	"github.com/epheo/dotvirt/internal/stream"
)

// pipeDialer opens a console onto an in-memory VM side that swallows input.
type pipeDialer struct{}

func (pipeDialer) VNCConn(string, string) (net.Conn, error) {
	vmSide, proxySide := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, vmSide) }()
	return proxySide, nil
}

// wantSessionClose reads ws until it closes and requires the session close.
func wantSessionClose(t *testing.T, what string, ws *websocket.Conn) {
	t.Helper()
	for {
		_, _, err := ws.ReadMessage()
		if err == nil {
			continue // a frame already in flight
		}
		if ce := (*websocket.CloseError)(nil); !errors.As(err, &ce) || ce.Code != websocket.ClosePolicyViolation {
			t.Fatalf("open %s after logout ended with %v, want the session close", what, err)
		}
		return
	}
}

// A logout ends the session server-side, wired as main wires it: the open
// inventory stream and console close, and the same cookie is refused on REST
// and on the stream upgrade.
func TestLogoutEndsStreamsAndCookie(t *testing.T) {
	fc := newFakeCluster(t)
	s := fc.server(nil, Config{})
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		tr := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview).DeepCopy()
		tr.Status = authnv1.TokenReviewStatus{Authenticated: tr.Spec.Token == tenantToken, User: authnv1.UserInfo{Username: "tenant"}}
		return true, tr, nil
	})
	s.auth = auth.New(kube, []byte("0123456789abcdef0123456789abcdef"))
	kinds := []eventbus.Kind{eventbus.LiveChanged, eventbus.GitChanged}
	wake, _ := s.bus.Subscribe(append(kinds, eventbus.SessionEnded)...)
	hub := stream.NewHub(s.InventoryForIdentity, s.auth.SessionLive, wake, stream.RepoLevels(s.bus, eventbus.GitChanged, kinds...), "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	s.UseStream(hub)
	vncWake, _ := s.bus.Subscribe(eventbus.SessionEnded)
	vnc := stream.NewVNCProxy(func(string) (stream.VNCDialer, error) { return pipeDialer{}, nil }, s.auth.SessionLive, vncWake, "")
	go vnc.Run(ctx)
	s.UseVNC(vnc)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"token":"`+tenantToken+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "dotvirt_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("login %d set no session cookie", resp.StatusCode)
	}
	send := func(method, path string) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.AddCookie(session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/inventory/stream"
	header := http.Header{"Cookie": {session.String()}}
	console, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/vms/team-a-dev/web/vnc", header)
	if err != nil {
		t.Fatalf("console before logout: %v", err)
	}
	defer func() { _ = console.Close() }()
	_ = console.SetReadDeadline(time.Now().Add(10 * time.Second))

	ws, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("stream upgrade before logout: %v", err)
	}
	defer func() { _ = ws.Close() }()
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, _, err := ws.ReadMessage(); err != nil {
		t.Fatalf("first frame: %v", err)
	}

	if code := send(http.MethodPost, "/api/logout"); code != http.StatusNoContent {
		t.Fatalf("logout: %d, want 204", code)
	}
	wantSessionClose(t, "stream", ws)
	wantSessionClose(t, "console", console)

	if code := send(http.MethodGet, "/api/inventory"); code != http.StatusUnauthorized {
		t.Errorf("signed-out cookie on REST: %d, want 401", code)
	}
	_, resp, err = websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("signed-out cookie on the stream upgrade: %v (%v), want 401", err, resp)
	}
}

// An SSO session's logout deletes its OAuth access token cluster-wide, under
// the caller's own token, by the name the oauth-apiserver stores it under. A
// pasted token is never deleted, and a failed delete still ends the session.
func TestSSOLogoutRevokesToken(t *testing.T) {
	// The oauth-apiserver's derivation: sha256~ plus the unpadded base64url
	// SHA-256 of the token after its prefix; sha256("abc") is the FIPS 180-2 vector.
	const token = "sha256~abc"
	const object = "/apis/oauth.openshift.io/v1/useroauthaccesstokens/sha256~ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0"
	cases := []struct {
		name       string
		sso        bool
		status     int
		wantDelete bool
	}{
		{"sso", true, http.StatusOK, true},
		{"pasted", false, http.StatusOK, false},
		{"delete fails", true, http.StatusInternalServerError, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := newFakeCluster(t)
			var by string
			fc.on(http.MethodDelete, object, func(w http.ResponseWriter, r *http.Request) {
				by = bearer(r)
				if c.status != http.StatusOK {
					writeStatus(w, c.status, metav1.StatusReasonInternalError)
					return
				}
				writeObj(w, metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess})
			})
			s := fc.server(nil, Config{})
			s.auth = auth.New(fake.NewSimpleClientset(), []byte("0123456789abcdef0123456789abcdef"))
			mux := http.NewServeMux()
			s.routes(mux)
			id := auth.Identity{Token: token, Username: "alice", Issued: time.Now(), SSO: c.sso}
			r := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, r.WithContext(auth.NewContext(r.Context(), id)))

			if rec.Code != http.StatusNoContent {
				t.Fatalf("logout: %d, want 204", rec.Code)
			}
			if s.auth.SessionLive(restfactory.TokenKey(token), id.Issued) {
				t.Error("the session outlived its logout")
			}
			if deleted := fc.called(http.MethodDelete, object); deleted != c.wantDelete {
				t.Fatalf("token delete sent = %v, want %v (calls %v)", deleted, c.wantDelete, fc.seen())
			}
			if c.wantDelete && by != token {
				t.Errorf("delete ran as %q, want the caller's own token", by)
			}
		})
	}
}
