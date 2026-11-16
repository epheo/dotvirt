package forge

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
