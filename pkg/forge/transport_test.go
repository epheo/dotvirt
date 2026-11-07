package forge

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Both TLS modes must keep the default transport's proxy and timeouts: a forge
// reached through HTTPS_PROXY is unreachable otherwise.
func TestHTTPClientKeepsDefaults(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, hc := range map[string]*http.Client{
		"insecure": httpClient(true, ""),
		"ca":       httpClient(false, ca),
	} {
		tr, ok := hc.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("%s: transport is %T", name, hc.Transport)
		}
		if tr.TLSClientConfig == nil {
			t.Errorf("%s: TLS config not applied", name)
		}
		if tr.Proxy == nil {
			t.Errorf("%s: proxy from environment dropped", name)
		}
		if tr.DialContext == nil || tr.TLSHandshakeTimeout == 0 || tr.IdleConnTimeout == 0 {
			t.Errorf("%s: dial/TLS/idle timeouts dropped", name)
		}
	}
}
