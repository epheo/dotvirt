package git

import (
	"crypto/tls"
	"net/http"

	gittransport "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/epheo/dotvirt/pkg/forge"
)

// AllowInsecureTLS makes go-git skip TLS certificate verification for https
// remotes. For dev only (e.g. a Forgejo Route with a self-signed cluster cert);
// never enable against untrusted networks.
func AllowInsecureTLS() {
	installHTTPS(&tls.Config{InsecureSkipVerify: true}) // #nosec G402 - dev flag
}

// AllowCustomCA: go-git trusts caFile (the ingress CA serving a managed forge
// Route) as the sole root, right for this single-forge process.
func AllowCustomCA(caFile string) {
	pool := forge.RootCAs("git", caFile)
	if pool == nil {
		return
	}
	installHTTPS(&tls.Config{RootCAs: pool})
}

func installHTTPS(tlsCfg *tls.Config) {
	client := &http.Client{Transport: transportWith(tlsCfg)}
	gittransport.InstallProtocol("https", githttp.NewClient(client))
}

// transportWith keeps the default transport's proxy and dial/TLS/idle
// timeouts; a bare Transport would drop them and hang on a dead remote.
func transportWith(tlsCfg *tls.Config) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = tlsCfg
	return t
}
