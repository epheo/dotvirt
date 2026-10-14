package restfactory

import (
	"os"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/epheo/dotvirt/internal/ttlcache"
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
	// No credential of any kind and no mounted token file -> nil.
	if cfg := saConfig(&rest.Config{}); cfg != nil {
		// In CI there's normally no /var/run/secrets mount, so this should be nil;
		// guard against a host that happens to have one.
		if _, err := os.Stat(saTokenPath); err != nil {
			t.Errorf("no SA credential → nil, got %+v", cfg)
		}
	}
}

func recordingFactory(t *testing.T, sa *rest.Config) (*Factory[*rest.Config], *int) {
	t.Helper()
	builds := 0
	full := &rest.Config{Host: "https://api.example:6443", BearerToken: "sa-secret"}
	return &Factory[*rest.Config]{
		base:     withLimits(clearCredentials(full), userQPS, userBurst),
		saConfig: withLimits(saConfig(sa), saQPS, saBurst),
		build:    func(c *rest.Config) (*rest.Config, error) { builds++; return c, nil },
		cache:    ttlcache.New[*rest.Config](clientTTL),
	}, &builds
}

// For must carry exactly the caller's token over the credential-less base, sized
// limits, and reuse one client per token.
func TestForCarriesOnlyCallerToken(t *testing.T) {
	f, builds := recordingFactory(t, &rest.Config{BearerToken: "sa-secret"})

	a, err := f.For("alice")
	if err != nil {
		t.Fatal(err)
	}
	if a.BearerToken != "alice" || a.BearerTokenFile != "" {
		t.Errorf("For(alice) carries token %q file %q", a.BearerToken, a.BearerTokenFile)
	}
	if a.QPS != userQPS || a.Burst != userBurst {
		t.Errorf("For limits = %v/%d, want %v/%d", a.QPS, a.Burst, userQPS, userBurst)
	}
	if f.base.BearerToken != "" {
		t.Error("For mutated the shared base config")
	}

	again, _ := f.For("alice")
	b, _ := f.For("bob")
	if again != a || *builds != 2 {
		t.Errorf("want one cached client per token, got %d builds", *builds)
	}
	if b.BearerToken != "bob" {
		t.Errorf("For(bob) carries token %q", b.BearerToken)
	}
}

func TestSABuildsOnceWithSALimits(t *testing.T) {
	f, builds := recordingFactory(t, &rest.Config{BearerTokenFile: "/var/run/token"})

	c, err := f.SA()
	if err != nil {
		t.Fatal(err)
	}
	if c.BearerTokenFile != "/var/run/token" || c.QPS != saQPS || c.Burst != saBurst {
		t.Errorf("SA config = file %q limits %v/%d", c.BearerTokenFile, c.QPS, c.Burst)
	}
	if again, _ := f.SA(); again != c || *builds != 1 {
		t.Errorf("SA client should be built once, got %d builds", *builds)
	}
}

func TestSAWithoutCredentialsFails(t *testing.T) {
	if _, err := os.Stat(saTokenPath); err == nil {
		t.Skip("host has a mounted SA token")
	}
	f, _ := recordingFactory(t, &rest.Config{})
	if _, err := f.SA(); err == nil {
		t.Error("SA without credentials should fail")
	}
}
