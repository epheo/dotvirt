package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The trace request contract: every malformed shape is rejected as a 400
// before any project resolution or cluster access happens.
func TestHandleTraceValidation(t *testing.T) {
	s := NewServer(Deps{})
	cases := []struct {
		name string
		body string
		want string
	}{
		{"malformed json", `{`, "invalid request body"},
		{"missing source", `{"destination":{"ip":"10.0.0.1"}}`, "source namespace and vm are required"},
		{"no destination", `{"source":{"namespace":"a","vm":"web"},"destination":{}}`, "destination must be a vm or an ip"},
		{"both destinations", `{"source":{"namespace":"a","vm":"web"},"destination":{"namespace":"b","vm":"db","ip":"10.0.0.1"}}`, "destination must be a vm or an ip"},
		{"bad ip", `{"source":{"namespace":"a","vm":"web"},"destination":{"ip":"not-an-ip"}}`, "invalid destination ip"},
		{"bad protocol", `{"source":{"namespace":"a","vm":"web"},"destination":{"ip":"10.0.0.1"},"protocol":"ICMP"}`, "protocol must be TCP, UDP or SCTP"},
		{"port too large", `{"source":{"namespace":"a","vm":"web"},"destination":{"ip":"10.0.0.1"},"port":70000}`, "invalid port"},
		{"negative port", `{"source":{"namespace":"a","vm":"web"},"destination":{"ip":"10.0.0.1"},"port":-1}`, "invalid port"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/networking/trace", strings.NewReader(c.body))
			s.handleTrace(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.want) {
				t.Errorf("body = %q, want %q", rec.Body.String(), c.want)
			}
		})
	}
}

// handlePermissions requires an explicit namespace before doing anything else.
func TestHandlePermissionsRequiresNamespace(t *testing.T) {
	s := NewServer(Deps{})
	rec := httptest.NewRecorder()
	s.handlePermissions(rec, httptest.NewRequest(http.MethodGet, "/api/permissions", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
