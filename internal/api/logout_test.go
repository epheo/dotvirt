package api

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// ssoToken is the access token the fake OAuth server issues. Its object name is
// the oauth-apiserver's: sha256~ plus the unpadded base64url SHA-256 of the
// token after its prefix; sha256("abc") is the FIPS 180-2 vector.
const (
	ssoToken  = "sha256~abc"
	ssoObject = "/apis/oauth.openshift.io/v1/useroauthaccesstokens/sha256~ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0"
)

// ssoStack serves the whole handler chain with OpenShift SSO wired to a fake
// OAuth server, and the token delete routed to del on the fake apiserver.
func ssoStack(t *testing.T, del http.HandlerFunc) (*httptest.Server, *Server) {
	t.Helper()
	oauthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+ssoToken+`","token_type":"Bearer"}`)
	}))
	t.Cleanup(oauthSrv.Close)
	fc := newFakeCluster(t)
	fc.on(http.MethodGet, "/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, map[string]string{"authorization_endpoint": oauthSrv.URL + "/authorize", "token_endpoint": oauthSrv.URL + "/token"})
	})
	fc.on(http.MethodDelete, ssoObject, del)
	s := fc.server(nil, Config{})
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		tr := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview).DeepCopy()
		tr.Status = authnv1.TokenReviewStatus{Authenticated: tr.Spec.Token == ssoToken, User: authnv1.UserInfo{Username: "alice"}}
		return true, tr, nil
	})
	s.auth = auth.New(kube, []byte("0123456789abcdef0123456789abcdef"))
	saKube, err := s.clusterF.SAKube()
	if err != nil {
		t.Fatal(err)
	}
	if s.oauth, err = auth.NewOAuth(auth.OAuthConfig{ClientID: "dotvirt", ClientSecret: "s3cret", RedirectURL: "https://dotvirt.example/api/auth/callback"}, saKube, s.auth); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s
}

// sessionCookie returns the session cookie resp set.
func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == "dotvirt_session" && c.MaxAge >= 0 {
			return c
		}
	}
	t.Fatalf("%s %s: %d set no session cookie", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode)
	return nil
}

// ssoSignIn runs the OpenShift sign-in: the redirect, then the callback with
// its state.
func ssoSignIn(t *testing.T, srv *httptest.Server) *http.Cookie {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/api/auth/openshift")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	loc, err := resp.Location()
	if err != nil {
		t.Fatalf("sign-in redirect %d: %v", resp.StatusCode, err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/callback?code=c&state="+loc.Query().Get("state"), nil)
	for _, c := range resp.Cookies() {
		req.AddCookie(c)
	}
	if resp, err = client.Do(req); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return sessionCookie(t, resp)
}

// pastedSignIn signs in with the same token pasted, as from oc whoami -t.
func pastedSignIn(t *testing.T, srv *httptest.Server) *http.Cookie {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"token":"`+ssoToken+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return sessionCookie(t, resp)
}

func logout(t *testing.T, ctx context.Context, srv *httptest.Server, c *http.Cookie) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/logout", nil)
	req.AddCookie(c)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// An SSO session's logout deletes its OAuth access token cluster-wide, under
// that token, by the name the oauth-apiserver stores it under. The same token
// pasted is never deleted, and a failed delete still ends the session.
func TestSSOLogoutRevokesToken(t *testing.T) {
	cases := []struct {
		name       string
		signIn     func(*testing.T, *httptest.Server) *http.Cookie
		status     int
		wantDelete bool
	}{
		{"sso", ssoSignIn, http.StatusOK, true},
		{"pasted", pastedSignIn, http.StatusOK, false},
		{"delete fails", ssoSignIn, http.StatusInternalServerError, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deletes := make(chan string, 1)
			srv, _ := ssoStack(t, func(w http.ResponseWriter, r *http.Request) {
				deletes <- bearer(r)
				if c.status != http.StatusOK {
					writeStatus(w, c.status, metav1.StatusReasonInternalError)
					return
				}
				writeObj(w, metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess})
			})
			cookie := c.signIn(t, srv)
			if code := logout(t, context.Background(), srv, cookie); code != http.StatusNoContent {
				t.Fatalf("logout: %d, want 204", code)
			}
			// The delete runs after the flushed 204; the next request waits on it.
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/me", nil)
			req.AddCookie(cookie)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("session after its logout: %d, want 401", resp.StatusCode)
			}
			if !c.wantDelete {
				// Its handler answered without a delete step.
				if len(deletes) != 0 {
					t.Fatal("deleted a pasted token")
				}
				return
			}
			select {
			case by := <-deletes:
				if by != ssoToken {
					t.Errorf("delete ran as %q, want the session's own token", by)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("SSO token never deleted")
			}
		})
	}
}

// logBuffer collects the standard logger's output, where a failed token
// delete is reported.
type logBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *logBuffer {
	t.Helper()
	buf, prev := &logBuffer{}, log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

const notRevoked = "SSO token not revoked"

// The SSO logout answers before its token delete, and the delete outlives a
// client that hangs up but not revokeTimeout.
func TestSSOLogoutDeleteDetachedAndBounded(t *testing.T) {
	t.Run("client hangs up", func(t *testing.T) {
		logs := captureLog(t)
		entered, release := make(chan struct{}), make(chan struct{})
		srv, _ := ssoStack(t, func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			writeObj(w, metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess})
		})
		var once sync.Once
		t.Cleanup(func() { once.Do(func() { close(release) }) })
		cookie := ssoSignIn(t, srv)
		ctx, hangUp := context.WithCancel(context.Background())
		answered := make(chan int, 1)
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/logout", nil)
			req.AddCookie(cookie)
			resp, err := srv.Client().Do(req)
			if err != nil {
				answered <- 0
				return
			}
			_ = resp.Body.Close()
			answered <- resp.StatusCode
		}()
		<-entered
		select {
		case code := <-answered:
			if code != http.StatusNoContent {
				t.Fatalf("logout: %d, want 204", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the logout answer waited on the token delete")
		}
		hangUp()
		srv.Client().CloseIdleConnections()
		time.Sleep(200 * time.Millisecond) // the server notices the client left
		if strings.Contains(logs.String(), notRevoked) {
			t.Fatalf("the client leaving cut the token delete short: %s", logs)
		}
		once.Do(func() { close(release) })
	})
	t.Run("delete stalls", func(t *testing.T) {
		logs := captureLog(t)
		release := make(chan struct{})
		srv, s := ssoStack(t, func(w http.ResponseWriter, r *http.Request) { <-release })
		t.Cleanup(func() { close(release) })
		s.revokeTimeout = 100 * time.Millisecond
		if code := logout(t, context.Background(), srv, ssoSignIn(t, srv)); code != http.StatusNoContent {
			t.Fatalf("logout: %d, want 204", code)
		}
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(logs.String(), notRevoked); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("a stalled token delete outlived revokeTimeout")
			}
		}
	})
}
