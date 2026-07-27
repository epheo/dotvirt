// Package tlsconf loads optional CA bundles under one shared tolerance rule: a
// bad or lagging bundle logs and returns nil, so the caller stays on the system
// trust pool and a missing CA mount degrades to a legible TLS error, never a
// crash.
package tlsconf

import (
	"crypto/x509"
	"log"
	"os"
)

// RootCAs returns a pool holding caFile's certificates, or nil when the file is
// unreadable or holds none (logged under component).
func RootCAs(component, caFile string) *x509.CertPool {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		log.Printf("%s: CA %s unreadable (%v); staying on the system trust pool", component, caFile, err)
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		log.Printf("%s: CA %s holds no certificates; staying on the system trust pool", component, caFile)
		return nil
	}
	return pool
}
