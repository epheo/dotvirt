package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A state-changing request from a sibling site is refused before any handler:
// every Route under one apps domain is same-site, so SameSite=Lax alone lets it
// through. Requests without browser fetch metadata (the forge webhook, the
// ApplicationSet plugin) and the configured UI origin still pass.
func TestCrossOriginProtection(t *testing.T) {
	reached := 0
	h := withCrossOriginProtection("http://localhost:5173", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
	}))
	cases := []struct {
		name, method, site, origin string
		want                       int
	}{
		{"sibling site POST", http.MethodPost, "same-site", "https://evil.apps.example", http.StatusForbidden},
		{"cross site DELETE", http.MethodDelete, "cross-site", "https://evil.example", http.StatusForbidden},
		{"old browser foreign Origin", http.MethodPost, "", "https://evil.apps.example", http.StatusForbidden},
		{"same origin POST", http.MethodPost, "same-origin", "https://dotvirt.apps.example", http.StatusOK},
		{"trusted UI origin", http.MethodPost, "cross-site", "http://localhost:5173", http.StatusOK},
		{"non-browser webhook", http.MethodPost, "", "", http.StatusOK},
		{"cross site GET", http.MethodGet, "cross-site", "https://evil.example", http.StatusOK},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "https://dotvirt.apps.example/api/vms", nil)
		if c.site != "" {
			r.Header.Set("Sec-Fetch-Site", c.site)
		}
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.want)
		}
	}
	if reached != 4 {
		t.Errorf("handler reached %d times, want 4", reached)
	}
}

// The full handler chain applies the check: a sibling-site POST never reaches
// the route, and the forge webhook (no browser headers) still does.
func TestHandlerRefusesSiblingSiteWrite(t *testing.T) {
	s := draftServer(t, &stagingDraft{})
	r := asUser(httptest.NewRequest(http.MethodPost, "/api/draft/propose?project=team-a", strings.NewReader("{}")), "admin-token")
	r.Header.Set("Sec-Fetch-Site", "same-site")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("sibling-site propose = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/webhooks/forge", strings.NewReader("{}")))
	if rec.Code == http.StatusForbidden {
		t.Errorf("webhook refused by the cross-origin check: %q", rec.Body.String())
	}
}

func TestOversizedBodyIs413(t *testing.T) {
	s := draftServer(t, &stagingDraft{})
	body := `{"namespace":"` + strings.Repeat("x", 1<<20) + `"}`
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost, "/api/vms", strings.NewReader(body)), "admin-token"))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d %q, want 413", rec.Code, rec.Body.String())
	}
}

// A container read naming a project or namespace outside the caller's view is
// a 404, not an empty scope.
func TestContainerScopeUnknownIsNotFound(t *testing.T) {
	s := draftServer(t, &stagingDraft{})
	for _, q := range []string{"?project=ghost", "?namespace=ghost"} {
		rec := httptest.NewRecorder()
		if _, _, ok := s.containerScope(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/events"+q, nil), "admin-token")); ok {
			t.Errorf("%s resolved", q)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", q, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	if _, nss, ok := s.containerScope(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/events", nil), "admin-token")); !ok || len(nss) != 0 {
		t.Errorf("unscoped read: ok=%v nss=%v, want ok with no namespaces", ok, nss)
	}
}
