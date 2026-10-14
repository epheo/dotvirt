package git

import (
	"crypto/tls"
	"testing"
)

func TestTransportWithKeepsDefaults(t *testing.T) {
	cfg := &tls.Config{InsecureSkipVerify: true} // #nosec G402 - test
	tr := transportWith(cfg)
	if tr.TLSClientConfig != cfg {
		t.Error("TLS config not applied")
	}
	if tr.Proxy == nil {
		t.Error("proxy from environment dropped")
	}
	if tr.DialContext == nil || tr.TLSHandshakeTimeout == 0 || tr.IdleConnTimeout == 0 {
		t.Error("dial/TLS/idle timeouts dropped")
	}
}
