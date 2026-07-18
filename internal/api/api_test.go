package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// statusFor is the one error-to-HTTP mapping every route shares; each model.Err
// kind must keep its status and anything unclassified must stay a 500.
func TestStatusFor(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: bad spec", model.ErrInvalid), http.StatusBadRequest},
		{fmt.Errorf("%w: no vm", model.ErrNotFound), http.StatusNotFound},
		{fmt.Errorf("%w: nope", model.ErrForbidden), http.StatusForbidden},
		{fmt.Errorf("%w: exists", model.ErrConflict), http.StatusConflict},
		{fmt.Errorf("%w: git", model.ErrUnavailable), http.StatusServiceUnavailable},
		{errors.New("a kubeconfig path leaked here"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		if got := statusFor(c.err); got != c.want {
			t.Errorf("statusFor(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// fail must echo a classified error's message but MASK an unclassified one —
// internal errors can carry k8s/git/forge internals the caller must not see.
func TestFailMasksInternalDetail(t *testing.T) {
	rec := httptest.NewRecorder()
	fail(rec, fmt.Errorf("%w: project name is required", model.ErrInvalid))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "project name is required") {
		t.Errorf("classified error not echoed: %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	fail(rec, errors.New("dial https://forge.internal:3000: secret-token"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret-token") {
		t.Errorf("internal detail echoed to the caller: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "internal error") {
		t.Errorf("masked body = %q, want the generic message", rec.Body.String())
	}
}

func TestRespond(t *testing.T) {
	rec := httptest.NewRecorder()
	respond(rec, map[string]string{"a": "b"}, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("ok response: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `"a":"b"`) {
		t.Errorf("body = %q", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	respond(rec, nil, fmt.Errorf("%w: gone", model.ErrNotFound))
	if rec.Code != http.StatusNotFound {
		t.Errorf("error response status = %d, want 404", rec.Code)
	}
}

// unavailable's public message must name only WHAT failed — transport errors
// embed endpoints and credentials that stay in the log.
func TestUnavailableNamesOnlyTheSubsystem(t *testing.T) {
	err := unavailable("cluster access", errors.New("dial tcp 10.0.0.1:6443: token=abc"))
	if !errors.Is(err, model.ErrUnavailable) {
		t.Fatal("kind lost")
	}
	if strings.Contains(err.Error(), "token=abc") {
		t.Errorf("transport detail leaked into the public error: %v", err)
	}
}

// spaRouter: /api/* reaches the API handler, a real static file is served
// as-is, and any other path falls back to index.html so client routes resolve.
func TestSPARouter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("the-index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("the-asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := spaRouter(dir, api)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	if rec := get("/api/vms"); rec.Code != http.StatusTeapot {
		t.Errorf("/api/* bypassed the API handler: %d", rec.Code)
	}
	if rec := get("/app.js"); !strings.Contains(rec.Body.String(), "the-asset") {
		t.Errorf("static file not served: %q", rec.Body.String())
	}
	if rec := get("/vm/prod/web-1"); !strings.Contains(rec.Body.String(), "the-index") {
		t.Errorf("client route did not fall back to index: %q", rec.Body.String())
	}
	// Traversal must not escape the static dir: the join is cleaned and
	// ServeFile's own dot-dot guard refuses the raw path outright.
	if rec := get("/../secret"); rec.Code != http.StatusBadRequest {
		t.Errorf("traversal path answered %d: %q", rec.Code, rec.Body.String())
	}
}

func TestWithCORS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// No configured origin: passthrough, no CORS headers.
	rec := httptest.NewRecorder()
	withCORS("", next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("headers set without a configured origin")
	}

	// Credentials mode requires echoing the specific origin — never "*".
	rec = httptest.NewRecorder()
	withCORS("http://localhost:5173", next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("origin = %q", got)
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("credentials header missing")
	}

	// Preflight is answered without invoking the wrapped handler.
	rec = httptest.NewRecorder()
	hit := false
	withCORS("http://localhost:5173", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit = true
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/me", nil))
	if rec.Code != http.StatusNoContent || hit {
		t.Errorf("preflight: code=%d nextHit=%v", rec.Code, hit)
	}
}
