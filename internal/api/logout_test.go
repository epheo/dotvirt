package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/stream"
)

// A logout ends the session server-side, wired as main wires it: the open
// inventory stream closes, and the same cookie is refused on REST and on the
// stream upgrade.
func TestLogoutEndsStreamAndCookie(t *testing.T) {
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
	for {
		_, _, err := ws.ReadMessage()
		if err == nil {
			continue // a frame already in flight
		}
		if ce := (*websocket.CloseError)(nil); !errors.As(err, &ce) || ce.Code != websocket.ClosePolicyViolation {
			t.Fatalf("open stream after logout ended with %v, want the session-rejected close", err)
		}
		break
	}

	if code := send(http.MethodGet, "/api/inventory"); code != http.StatusUnauthorized {
		t.Errorf("signed-out cookie on REST: %d, want 401", code)
	}
	_, resp, err = websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("signed-out cookie on the stream upgrade: %v (%v), want 401", err, resp)
	}
}
