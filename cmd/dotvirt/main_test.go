package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// An upgraded socket idle past IdleTimeout must stay open: the timeout must only
// reap idle keep-alives, never the live stream.
func TestIdleTimeoutSparesUpgradedConns(t *testing.T) {
	if newHTTPServer(":0", nil).IdleTimeout <= 0 {
		t.Fatal("IdleTimeout unset")
	}
	var up websocket.Upgrader
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			_ = c.WriteMessage(mt, msg)
		}
	})
	ts := httptest.NewUnstartedServer(h)
	ts.Config = newHTTPServer("", h)
	ts.Config.IdleTimeout = 20 * time.Millisecond
	ts.Start()
	defer ts.Close()

	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	time.Sleep(200 * time.Millisecond)
	if err := c.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, msg, err := c.ReadMessage(); err != nil || string(msg) != "ping" {
		t.Fatalf("echo after idle: %q %v", msg, err)
	}
}
