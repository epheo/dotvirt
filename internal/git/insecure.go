package git

import (
	"crypto/tls"
	"crypto/x509"
	"log"
	"net/http"
	"os"

	gittransport "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// AllowInsecureTLS makes go-git skip TLS certificate verification for https
// remotes. For dev only (e.g. a Forgejo Route with a self-signed cluster cert);
// never enable against untrusted networks.
func AllowInsecureTLS() {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 — dev flag
		},
	}
	gittransport.InstallProtocol("https", githttp.NewClient(client))
}

// AllowCustomCA makes go-git trust caFile's PEM bundle (typically the cluster's
// ingress CA serving a managed forge Route) IN ADDITION to nothing else — the pool
// replaces the system roots for git https, which is correct for the single-forge
// client this process is. Tolerant: an unreadable or empty bundle logs and leaves
// the system pool in place, so a lagging CA mount degrades to a TLS error at the
// forge (legible in every propose) instead of a crashlooping pod.
func AllowCustomCA(caFile string) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		log.Printf("git: forge CA %s unreadable (%v); staying on the system trust pool", caFile, err)
		return
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		log.Printf("git: forge CA %s holds no certificates; staying on the system trust pool", caFile)
		return
	}
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	gittransport.InstallProtocol("https", githttp.NewClient(client))
}
