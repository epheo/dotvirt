package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testIndex = `<!doctype html>
<html><head>
<script>console.log("theme")</script>
<script type="module" src="/_app/start.js"></script>
<script>__sveltekit = {}</script>
</head><body></body></html>`

func TestBuildCSPHashesInlineScripts(t *testing.T) {
	csp := buildCSP([]byte(testIndex), "")

	// Exactly the two bare inline scripts are hashed; the src= one rides on 'self'.
	if n := strings.Count(csp, "'sha256-"); n != 2 {
		t.Errorf("want 2 script hashes, got %d in %q", n, csp)
	}
	// sha256(`console.log("theme")`)
	if !strings.Contains(csp, "'sha256-BsX0E4Aunyt7x/vhF8ZiSpsfyifYgukNmxkd4Bub/Yg='") {
		t.Errorf("missing hash of the first inline script: %q", csp)
	}
	for _, d := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, d) {
			t.Errorf("missing directive %q in %q", d, csp)
		}
	}
	if strings.Contains(csp, "connect-src 'self';") == false {
		t.Errorf("connect-src should be bare 'self' without an upload proxy: %q", csp)
	}
}

func TestBuildCSPUploadProxyOrigin(t *testing.T) {
	csp := buildCSP([]byte(testIndex), "https://cdi-uploadproxy.apps.example.com/")
	if !strings.Contains(csp, "connect-src 'self' https://cdi-uploadproxy.apps.example.com") {
		t.Errorf("upload proxy origin missing from connect-src: %q", csp)
	}

	// A malformed proxy URL must not widen the policy.
	csp = buildCSP([]byte(testIndex), "::bad::")
	if !strings.Contains(csp, "connect-src 'self';") {
		t.Errorf("malformed proxy URL widened connect-src: %q", csp)
	}
}

func TestWithSecurityHeaders(t *testing.T) {
	h := withSecurityHeaders("default-src 'self'", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "same-origin",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	// No CSP computed (index.html unreadable): the other headers still apply.
	h = withSecurityHeaders("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get("Content-Security-Policy") != "" {
		t.Error("empty csp must not set the header")
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing without csp")
	}
}

// TestWithBodyLimit pins the request-body cap: an oversized body must error at
// the decoder instead of being buffered, and a normal body passes untouched.
func TestWithBodyLimit(t *testing.T) {
	h := withBodyLimit(64, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(strings.Repeat("A", 1024))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: status = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"t"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("small body: status = %d, want 200", rec.Code)
	}
}
