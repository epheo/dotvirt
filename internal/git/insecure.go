package git

import (
	"crypto/tls"
	"net/http"

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
