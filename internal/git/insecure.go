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

// AllowCustomCA: go-git trusts caFile (the ingress CA serving a managed forge
// Route) as the sole root, right for this single-forge process. Tolerant: a bad
// bundle logs and keeps the system pool, so a lagging CA mount degrades to a
// legible TLS error, never a crashlooping pod.
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
