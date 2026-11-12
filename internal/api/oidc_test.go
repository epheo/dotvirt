package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

// issuerStub is an OIDC issuer whose tokens live two seconds and whose
// refresh tokens are spent once.
type issuerStub struct {
	srv     *httptest.Server
	mu      sync.Mutex
	nonce   string
	minted  map[string]bool
	live    map[string]bool
	revoked []string
	n       int
}

func newIssuerStub(t *testing.T) *issuerStub {
	t.Helper()
	is := &issuerStub{minted: map[string]bool{}, live: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeObj(w, map[string]string{
			"issuer": is.srv.URL, "authorization_endpoint": is.srv.URL + "/authorize",
			"token_endpoint": is.srv.URL + "/token", "revocation_endpoint": is.srv.URL + "/revoke",
		})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		is.mu.Lock()
		defer is.mu.Unlock()
		var nonce string
		switch r.PostFormValue("grant_type") {
		case "authorization_code":
			nonce = is.nonce
		case "refresh_token":
			if !is.live[r.PostFormValue("refresh_token")] {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			delete(is.live, r.PostFormValue("refresh_token"))
		}
		is.n++
		claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(2 * time.Second).Unix(), "nonce": nonce, "n": is.n})
		id := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
		rt := fmt.Sprintf("r%d", is.n)
		is.minted[id], is.live[rt] = true, true
		writeObj(w, map[string]any{"access_token": "a", "token_type": "Bearer", "id_token": id, "refresh_token": rt})
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		is.mu.Lock()
		is.revoked = append(is.revoked, r.PostFormValue("token"))
		is.mu.Unlock()
	})
	is.srv = httptest.NewServer(mux)
	t.Cleanup(is.srv.Close)
	return is
}

func (is *issuerStub) mintedToken(token string) bool {
	is.mu.Lock()
	defer is.mu.Unlock()
	return is.minted[token]
}

// An OIDC session through the whole chain: its open stream builds each frame
// with the session's current token, not the one it opened with, and its
// logout closes the stream and revokes the newest refresh token.
func TestOIDCSessionStreamsWithItsCurrentToken(t *testing.T) {
	is := newIssuerStub(t)
	fc := newFakeCluster(t)
	var presentedMu sync.Mutex
	presented := map[string]bool{}
	fc.readsVMs = func(token, ns string) bool {
		presentedMu.Lock()
		presented[token] = true
		presentedMu.Unlock()
		return false // the view's probe is what this test watches, not what it shows
	}
	s := fc.server(nil, Config{})
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		tr := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview).DeepCopy()
		tr.Status = authnv1.TokenReviewStatus{Authenticated: is.mintedToken(tr.Spec.Token), User: authnv1.UserInfo{Username: "uw:alice"}}
		return true, tr, nil
	})
	s.auth = auth.New(kube, []byte("0123456789abcdef0123456789abcdef"))
	s.oidc = auth.NewOIDC(auth.OIDCConfig{Issuer: is.srv.URL, ClientID: "dotvirt", ClientSecret: "s3cret", RedirectURL: "https://dotvirt.example/api/auth/callback", Name: "Kanidm"}, s.auth)
	kinds := []eventbus.Kind{eventbus.LiveChanged, eventbus.GitChanged, eventbus.RBACChanged}
	wake, _ := s.bus.Subscribe(append(kinds, eventbus.SessionEnded)...)
	hub := stream.NewHub(s.InventoryForIdentity, s.auth.SessionLive, wake, stream.RepoLevels(s.bus, eventbus.GitChanged, kinds...), "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	s.UseStream(hub)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	var methods authMethods
	resp, err := http.Get(srv.URL + "/api/auth/methods")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&methods)
	resp.Body.Close()
	if methods != (authMethods{OIDC: "Kanidm"}) {
		t.Fatalf("methods = %+v", methods)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = client.Get(srv.URL + "/api/auth/oidc")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	q := must(url.Parse(resp.Header.Get("Location"))).Query()
	is.mu.Lock()
	is.nonce = q.Get("nonce")
	is.mu.Unlock()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/callback?code=c&state="+url.QueryEscape(q.Get("state")), nil)
	for _, c := range resp.Cookies() {
		req.AddCookie(c)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	session := sessionCookie(t, resp)

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/inventory/stream", http.Header{"Cookie": {session.String()}})
	if err != nil {
		t.Fatalf("stream upgrade: %v", err)
	}
	defer func() { _ = ws.Close() }()
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	if _, _, err := ws.ReadMessage(); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	presentedMu.Lock()
	opened := len(presented)
	before := map[string]bool{}
	for tok := range presented {
		before[tok] = true
	}
	presentedMu.Unlock()
	if opened == 0 {
		t.Fatal("the first frame presented no token")
	}

	// Past every token's renewal time, a grant change makes the stream
	// rebuild its view under the session's token.
	time.Sleep(2100 * time.Millisecond)
	s.bus.Publish(eventbus.RBACChanged)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		presentedMu.Lock()
		var renewed bool
		for tok := range presented {
			renewed = renewed || !before[tok]
		}
		presentedMu.Unlock()
		if renewed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stream never presented its session's renewed token")
		}
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/logout", nil)
	req.AddCookie(session)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	wantSessionClose(t, "stream", ws)
	// The revocation follows the answer: the UI need not wait on the issuer.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		is.mu.Lock()
		revoked, want := append([]string(nil), is.revoked...), fmt.Sprintf("r%d", is.n)
		is.mu.Unlock()
		if len(revoked) > 0 {
			if len(revoked) != 1 || revoked[0] != want {
				t.Fatalf("revoked = %v, want [%s]", revoked, want)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("logout never revoked the refresh token")
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
