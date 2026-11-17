package forge

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A refusal is the forge declining the account (401, 403, 404); an outage or a
// transport failure is not, so its caller keeps retrying.
func TestRefusedStatusClassifies(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("dial tcp: connection refused"), 0},
		{&StatusError{Status: http.StatusInternalServerError}, 0},
		{&StatusError{Status: http.StatusBadGateway}, 0},
		{&StatusError{Status: http.StatusTooManyRequests}, 0},
		{&StatusError{Status: http.StatusUnauthorized}, http.StatusUnauthorized},
		{&StatusError{Status: http.StatusForbidden}, http.StatusForbidden},
		{&StatusError{Status: http.StatusNotFound}, http.StatusNotFound},
		{fmt.Errorf("wrapped: %w", &StatusError{Status: http.StatusForbidden}), http.StatusForbidden},
	} {
		if got := RefusedStatus(tc.err); got != tc.want {
			t.Errorf("RefusedStatus(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// The owner lookup's 403 reaches the caller as a refusal.
func TestOwnerLookupForbiddenIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := NewFactory(srv.URL, StaticToken("tok"), false, "").For("https://forge.example/acme/repo.git")
	if _, err := c.OwnerIsOrg(); !Refused(err) {
		t.Errorf("OwnerIsOrg error %v, want a refusal", err)
	}
	if _, err := c.HasOrgWebhook("https://dotvirt.example/api/webhooks/forge"); !Refused(err) {
		t.Errorf("HasOrgWebhook error %v, want a refusal", err)
	}
}

// The fingerprint follows the current token, and never contains it.
func TestCredentialFingerprintFollowsTheToken(t *testing.T) {
	tok := "one"
	f := NewFactory("https://forge.example", func() string { return tok }, false, "")
	a := f.CredentialFingerprint()
	tok = "two"
	b := f.CredentialFingerprint()
	if a == b || a == "" {
		t.Errorf("fingerprints %q and %q, want distinct and set", a, b)
	}
}

// The forge's own reason travels, bounded and printable, only from exactly
// one JSON object with a string message: a proxy's plain text or page, which
// can echo internal hosts and URLs, carries none.
func TestRefusalTextIsTheForgesBoundedReason(t *testing.T) {
	long := strings.Repeat("x", 400)
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&StatusError{Status: http.StatusForbidden, Message: `{"message":"token does not have at least one of required scope(s): [write:organization]","url":"https://forge.example/api/swagger"}`},
			"token does not have at least one of required scope(s): [write:organization]"},
		{&StatusError{Status: http.StatusForbidden, Message: `{"message":"Must be an organization owner\nsecond line"}`}, "Must be an organization owner"},
		{&StatusError{Status: http.StatusForbidden, Message: "Forbidden: POST http://10.0.0.5:3000/api/v1/orgs/x/hooks?token=abc"}, ""},
		{&StatusError{Status: http.StatusNotFound, Message: "<html><body>Not Found</body></html>"}, ""},
		{&StatusError{Status: http.StatusForbidden, Message: `{"error":"denied"}`}, ""},
		{&StatusError{Status: http.StatusForbidden, Message: `{"message":42}`}, ""},
		{&StatusError{Status: http.StatusForbidden, Message: `{"message":"ok"} trailing http://10.0.0.5`}, ""},
		{&StatusError{Status: http.StatusForbidden, Message: `{"message":"bad\u001b[31mcolor\u00e9"}`}, "bad[31mcolor"},
		{&StatusError{Status: http.StatusBadGateway, Message: `{"message":"upstream"}`}, ""},
		{errors.New("dial tcp"), ""},
		{&StatusError{Status: http.StatusForbidden, Message: `{"message":"` + long + `"}`}, long[:refusalTextMax] + "..."},
	} {
		if got := RefusalText(tc.err); got != tc.want {
			t.Errorf("RefusalText(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// A user account is told apart from no account at all.
func TestOwnerIsUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/users/alice" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	f := NewFactory(srv.URL, StaticToken("tok"), false, "")
	if ok, err := f.For("https://forge.example/alice/r.git").OwnerIsUser(); !ok || err != nil {
		t.Errorf("alice: %v %v, want a user", ok, err)
	}
	if ok, err := f.For("https://forge.example/ghost/r.git").OwnerIsUser(); ok || err != nil {
		t.Errorf("ghost: %v %v, want no account", ok, err)
	}
}
