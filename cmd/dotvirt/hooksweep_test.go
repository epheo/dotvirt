package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/epheo/dotvirt/pkg/forge"
)

// sweepForge answers the owner lookup with status and counts every call.
func sweepForge(t *testing.T, status *atomic.Int32, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch {
		case r.URL.Path == "/api/v1/orgs/acme":
			w.WriteHeader(int(status.Load()))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/acme/hooks":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/acme/hooks":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A refusal of the owner's webhooks repeats on every tick, so it is asked
// once per credential and logged with the hook to create by hand; a changed
// credential asks again.
func TestHookSweepHoldsARefusalUntilTheCredentialChanges(t *testing.T) {
	var status, calls atomic.Int32
	status.Store(http.StatusForbidden)
	srv := sweepForge(t, &status, &calls)
	token := "one"
	var logs []string
	h := &hookSweep{
		ff:      forge.NewFactory(srv.URL, func() string { return token }, false, ""),
		target:  "https://dotvirt.example/api/webhooks/forge",
		secret:  "hmac",
		refused: map[string]string{},
		logf:    func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
	}
	repos := []string{"https://forge.example/acme/platform.git"}

	h.run(repos)
	if calls.Load() != 1 {
		t.Fatalf("first sweep made %d calls, want the owner lookup", calls.Load())
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "https://dotvirt.example/api/webhooks/forge") ||
		!strings.Contains(logs[0], "push and pull_request") || strings.Contains(logs[0], "hmac") {
		t.Errorf("logs = %q, want one line naming the hook to create and never the secret", logs)
	}
	h.run(repos)
	h.run(repos)
	if calls.Load() != 1 {
		t.Errorf("held sweeps made %d calls in all, want none after the refusal", calls.Load())
	}

	token = "two"
	status.Store(http.StatusOK)
	h.run(repos)
	if calls.Load() != 4 {
		t.Errorf("after a credential change: %d calls in all, want lookup, list and create (4)", calls.Load())
	}
	if _, held := h.refused["org:acme"]; held {
		t.Error("a registered hook stays held")
	}
}

// An outage is not a refusal: the next tick asks again.
func TestHookSweepRetriesAnOutage(t *testing.T) {
	var status, calls atomic.Int32
	status.Store(http.StatusBadGateway)
	srv := sweepForge(t, &status, &calls)
	h := &hookSweep{
		ff:      forge.NewFactory(srv.URL, forge.StaticToken("tok"), false, ""),
		target:  "https://dotvirt.example/api/webhooks/forge",
		refused: map[string]string{},
		logf:    func(string, ...any) {},
	}
	repos := []string{"https://forge.example/acme/platform.git"}
	h.run(repos)
	h.run(repos)
	if calls.Load() != 2 {
		t.Errorf("%d calls over two sweeps, want the lookup each time", calls.Load())
	}
	if len(h.refused) != 0 {
		t.Errorf("an outage held %v", h.refused)
	}
}
