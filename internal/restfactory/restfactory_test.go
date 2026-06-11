package restfactory

import (
	"os"
	"testing"

	"k8s.io/client-go/rest"
)

// TestClearCredentials is the security-critical check: the base config a per-user
// token rides on must carry no ambient identity. Only the endpoint and the
// server-trust (CA) survive; every client-credential field is cleared.
func TestClearCredentials(t *testing.T) {
	full := &rest.Config{
		Host:            "https://api.example:6443",
		APIPath:         "/k8s",
		BearerToken:     "sa-secret",
		BearerTokenFile: "/var/run/token",
		Username:        "admin",
		Password:        "hunter2",
		TLSClientConfig: rest.TLSClientConfig{
			Insecure:   false,
			ServerName: "api.example",
			CAData:     []byte("ca"),
			CertData:   []byte("client-cert"),
			KeyData:    []byte("client-key"),
			CertFile:   "/c.crt",
			KeyFile:    "/c.key",
		},
	}

	base := clearCredentials(full)

	if base.BearerToken != "" || base.BearerTokenFile != "" {
		t.Error("base config leaked a bearer token")
	}
	if base.Username != "" || base.Password != "" {
		t.Error("base config leaked basic-auth credentials")
	}
	tc := base.TLSClientConfig
	if tc.CertData != nil || tc.KeyData != nil || tc.CertFile != "" || tc.KeyFile != "" {
		t.Error("base config leaked a client certificate")
	}
	if base.Host != full.Host || base.APIPath != full.APIPath {
		t.Error("base config dropped the endpoint")
	}
	if string(tc.CAData) != "ca" || tc.ServerName != "api.example" {
		t.Error("base config dropped the server trust (CA/ServerName)")
	}
}

// TestSAConfig checks the SA-identity config keeps credentials live: a token FILE
// is preferred (so client-go refreshes the rotating SA token) and a stale inline
// copy is cleared; with no credential at all it returns nil.
func TestSAConfig(t *testing.T) {
	// File-backed token: file kept, inline cleared (else client-go would prefer the
	// non-refreshing inline copy).
	if cfg := saConfig(&rest.Config{BearerTokenFile: "/var/run/token", BearerToken: "stale"}); cfg == nil ||
		cfg.BearerTokenFile != "/var/run/token" || cfg.BearerToken != "" {
		t.Errorf("file-backed SA config should keep the file and drop the inline token, got %+v", cfg)
	}
	// Inline-only token (e.g. a static kubeconfig): preserved as-is.
	if cfg := saConfig(&rest.Config{BearerToken: "inline"}); cfg == nil || cfg.BearerToken != "inline" {
		t.Errorf("inline-only SA config should be preserved, got %+v", cfg)
	}
	// Client cert only: treated as a valid SA credential (non-nil).
	if cfg := saConfig(&rest.Config{TLSClientConfig: rest.TLSClientConfig{CertData: []byte("c"), KeyData: []byte("k")}}); cfg == nil {
		t.Error("client-cert SA config should be valid (non-nil)")
	}
	// No credential of any kind and no mounted token file → nil.
	if cfg := saConfig(&rest.Config{}); cfg != nil {
		// In CI there's normally no /var/run/secrets mount, so this should be nil;
		// guard against a host that happens to have one.
		if _, err := os.Stat(saTokenPath); err != nil {
			t.Errorf("no SA credential → nil, got %+v", cfg)
		}
	}
}
