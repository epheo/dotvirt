package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epheo/dotvirt/pkg/forge"
)

// sweepForge answers the org lookup with org, the org's hooks with hooks, and
// a user-owned repo's hooks with repo, counting every call.
type sweepForge struct {
	org, hooks, repo atomic.Int32
	calls            atomic.Int32
}

func (f *sweepForge) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		answer := func(status int32) {
			if status != http.StatusOK {
				w.WriteHeader(int(status))
				_, _ = w.Write([]byte(`{"message":"Must be an organization owner"}`))
				return
			}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":1}`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		}
		switch r.URL.Path {
		case "/api/v1/orgs/acme", "/api/v1/orgs/alice":
			w.WriteHeader(int(f.org.Load()))
		case "/api/v1/orgs/acme/hooks":
			answer(f.hooks.Load())
		case "/api/v1/repos/alice/proj/hooks":
			answer(f.repo.Load())
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sweep builds a hookSweep against srv with a settable token and clock,
// recording its log lines.
func sweep(srv *httptest.Server, token *string, clock *time.Time, logs *[]string) *hookSweep {
	h := newHookSweep(forge.NewFactory(srv.URL, func() string { return *token }, false, ""),
		"https://dotvirt.example/api/webhooks/forge", "hmac-zq7w",
		func(f string, a ...any) { *logs = append(*logs, fmt.Sprintf(f, a...)) })
	h.now = func() time.Time { return *clock }
	return h
}

// The realistic refusal: the org answers its lookup, its hooks are refused to
// a non-owner. It is held, logged once with the forge's own reason and what to
// do, rechecked after an hour, and at once when the credential changes.
func TestHookSweepHoldsAnOrgHookRefusal(t *testing.T) {
	f := &sweepForge{}
	f.org.Store(http.StatusOK)
	f.hooks.Store(http.StatusForbidden)
	srv := f.serve(t)
	token, clock, logs := "tok-a", time.Unix(0, 0), []string{}
	h := sweep(srv, &token, &clock, &logs)
	repos := []string{"https://forge.example/acme/platform.git"}

	h.run(repos)
	if f.calls.Load() != 2 {
		t.Fatalf("first sweep made %d calls, want the lookup and the hooks", f.calls.Load())
	}
	if len(logs) != 1 {
		t.Fatalf("logs = %q, want one line", logs)
	}
	for _, want := range []string{"Must be an organization owner", "owner of the org", "https://dotvirt.example/api/webhooks/forge",
		"push and pull_request", "system hook", "oc extract secret/dotvirt-webhook", "rechecked hourly"} {
		if !strings.Contains(logs[0], want) {
			t.Errorf("log lacks %q: %s", want, logs[0])
		}
	}
	if strings.Contains(logs[0], "hmac-zq7w") || strings.Contains(logs[0], "tok-a") {
		t.Errorf("log carries a secret: %s", logs[0])
	}

	for range 5 { // fifty minutes of ticks
		clock = clock.Add(10 * time.Minute)
		h.run(repos)
	}
	if f.calls.Load() != 2 {
		t.Errorf("held ticks made %d calls, want none", f.calls.Load()-2)
	}
	clock = clock.Add(10 * time.Minute) // the hour is up
	h.run(repos)
	if f.calls.Load() != 4 {
		t.Errorf("after an hour: %d calls in all, want the forge asked again (4)", f.calls.Load())
	}

	token = "tok-b"
	f.hooks.Store(http.StatusOK)
	h.run(repos)
	if _, held := h.refused["org:acme"]; held {
		t.Error("a new credential's registration stays held")
	}
}

// A user-owned forge: each repo's hook is held on its own, and the owner's
// kind is not asked again while they are.
func TestHookSweepHoldsPerRepoRefusals(t *testing.T) {
	f := &sweepForge{}
	f.org.Store(http.StatusNotFound) // a user, not an org
	f.repo.Store(http.StatusForbidden)
	srv := f.serve(t)
	token, clock, logs := "tok", time.Unix(0, 0), []string{}
	h := sweep(srv, &token, &clock, &logs)
	repos := []string{"https://forge.example/alice/proj.git"}

	h.run(repos)
	asked := f.calls.Load()
	if asked != 2 {
		t.Fatalf("first sweep made %d calls, want the lookup and the repo's hooks", asked)
	}
	for range 5 {
		clock = clock.Add(10 * time.Minute)
		h.run(repos)
	}
	if f.calls.Load() != asked {
		t.Errorf("held ticks made %d calls, want none", f.calls.Load()-asked)
	}
	clock = clock.Add(10 * time.Minute)
	f.repo.Store(http.StatusOK)
	h.run(repos)
	if _, held := h.refused["repo:"+repos[0]]; held {
		t.Error("the repo stays held after the hour, once the forge allows it")
	}
}

// A rejected token reads as one, not as a missing permission.
func TestHookSweepNamesARejectedToken(t *testing.T) {
	f := &sweepForge{}
	f.org.Store(http.StatusUnauthorized)
	srv := f.serve(t)
	token, clock, logs := "tok", time.Unix(0, 0), []string{}
	h := sweep(srv, &token, &clock, &logs)
	h.run([]string{"https://forge.example/acme/platform.git"})
	if len(logs) != 1 || !strings.Contains(logs[0], "rejected dotvirt's forge token") || strings.Contains(logs[0], "owner of the org") {
		t.Errorf("logs = %q, want the token named as rejected", logs)
	}
}

// An outage is not a refusal: the next tick asks again.
func TestHookSweepRetriesAnOutage(t *testing.T) {
	f := &sweepForge{}
	f.org.Store(http.StatusBadGateway)
	srv := f.serve(t)
	token, clock, logs := "tok", time.Unix(0, 0), []string{}
	h := sweep(srv, &token, &clock, &logs)
	repos := []string{"https://forge.example/acme/platform.git"}
	h.run(repos)
	h.run(repos)
	if f.calls.Load() != 2 {
		t.Errorf("%d calls over two sweeps, want the lookup each time", f.calls.Load())
	}
	if len(h.refused) != 0 {
		t.Errorf("an outage held %v", h.refused)
	}
}
