// Package restfactory mints per-bearer-token Kubernetes clients off a single,
// credential-less base config — the shared identity machinery behind both the
// cluster and argo planes. A per-user token fully determines identity (cluster
// RBAC is the sole authority); dotvirt's own ServiceAccount token drives
// background work. What kind of client each token yields is the caller's concern,
// supplied as a build function; everything else (base config, SA-token capture,
// per-token caching) lives here once.
package restfactory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/epheo/dotvirt/internal/ttlcache"
)

// saTokenPath is where an in-cluster pod's ServiceAccount token is mounted.
const saTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// clientTTL bounds how long a per-token client is reused before rebuild: short so
// a revoked token stops working promptly, long enough to amortize a request burst.
const clientTTL = 5 * time.Minute

// Factory builds and caches clients of type T, one per bearer token.
type Factory[T any] struct {
	base     *rest.Config // credential-less; per-user tokens ride a copy of this
	saConfig *rest.Config // dotvirt's own SA identity, with file-based creds PRESERVED
	build    func(*rest.Config) (T, error)

	cache *ttlcache.Cache[T] // per-token clients, keyed by sha256(token)

	saMu     sync.Mutex
	saClient T // built once; the SA identity is stable for the process
	saBuilt  bool
}

// New builds a Factory. kubeconfig empty means in-cluster config. build turns a
// token-bearing rest.Config into the concrete client (kubevirt+kube+dyn, dynamic,
// …). The per-user path rides a credential-less base; the SA path keeps the
// resolved config's own credentials (crucially its BearerTokenFile) so client-go
// auto-refreshes the rotating projected SA token.
func New[T any](kubeconfig string, build func(*rest.Config) (T, error)) (*Factory[T], error) {
	full, err := restConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	return &Factory[T]{
		base:     clearCredentials(full),
		saConfig: saConfig(full),
		build:    build,
		cache:    ttlcache.New[T](clientTTL),
	}, nil
}

// For returns the client whose every call authenticates with token, cached by
// token hash with a short TTL.
func (f *Factory[T]) For(token string) (T, error) {
	key := TokenKey(token)
	if c, ok := f.cache.Get(key); ok {
		return c, nil
	}

	cfg := rest.CopyConfig(f.base)
	cfg.BearerToken = token
	client, err := f.build(cfg)
	if err != nil {
		var zero T
		return zero, err
	}

	f.cache.Put(key, client)
	return client, nil
}

// SA returns the client for dotvirt's own ServiceAccount (background identity).
// Built once from saConfig and reused: the SA identity is stable, and keeping the
// BearerTokenFile on saConfig lets client-go transparently re-read the token when
// the projected (rotating, ~1h) SA token is refreshed — so SA-identity work
// doesn't 401 an hour after startup. A static-token kubeconfig (no file) is reused
// as-is, which the user accepts by supplying a non-refreshing token.
func (f *Factory[T]) SA() (T, error) {
	if f.saConfig == nil {
		var zero T
		return zero, fmt.Errorf("no service-account credentials available (set -kubeconfig or run in-cluster)")
	}
	f.saMu.Lock()
	defer f.saMu.Unlock()
	if f.saBuilt {
		return f.saClient, nil
	}
	client, err := f.build(rest.CopyConfig(f.saConfig))
	if err != nil {
		var zero T
		return zero, err
	}
	f.saClient, f.saBuilt = client, true
	return client, nil
}

// restConfig is the fully-resolved config (with whatever creds the environment
// supplies); used to capture the SA token.
func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		return rest.InClusterConfig()
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

// clearCredentials projects a config down to endpoint + server-trust only: Host,
// APIPath, and the TLS *server* fields (CA/ServerName/Insecure) are kept; every
// client-identity field (bearer token, token file, basic auth, client cert/key)
// is dropped. This is the security boundary of the per-token factory.
func clearCredentials(full *rest.Config) *rest.Config {
	return &rest.Config{
		Host:    full.Host,
		APIPath: full.APIPath,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure:   full.TLSClientConfig.Insecure,
			ServerName: full.TLSClientConfig.ServerName,
			CAFile:     full.TLSClientConfig.CAFile,
			CAData:     full.TLSClientConfig.CAData,
		},
	}
}

// saConfig returns the config dotvirt uses as its own ServiceAccount, keeping the
// resolved config's credentials so they stay live. It prefers a token FILE
// (BearerTokenFile, or the conventional in-cluster mount) over an inline token,
// because client-go re-reads the file each request — the projected SA token
// rotates. Returns nil only when no SA credential exists at all (no token, no
// file, no client cert).
func saConfig(full *rest.Config) *rest.Config {
	cfg := rest.CopyConfig(full)
	if cfg.BearerTokenFile == "" && cfg.BearerToken == "" {
		// In-cluster configs set BearerTokenFile, but be defensive: point at the mount
		// if it exists so we get refresh rather than a one-shot read.
		if _, err := os.Stat(saTokenPath); err == nil {
			cfg.BearerTokenFile = saTokenPath
		}
	}
	// A file-backed token must not also carry a stale inline copy (client-go prefers
	// the inline BearerToken, which would defeat refresh).
	if cfg.BearerTokenFile != "" {
		cfg.BearerToken = ""
	}
	if cfg.BearerToken == "" && cfg.BearerTokenFile == "" && !hasClientCert(cfg) {
		return nil
	}
	return cfg
}

func hasClientCert(cfg *rest.Config) bool {
	tc := cfg.TLSClientConfig
	return tc.CertFile != "" || len(tc.CertData) > 0
}

// TokenKey is the stable cache key for a bearer token (sha256 hex). Exported so
// the auth layer keys its TokenReview cache the same way, rather than duplicating
// the hashing.
func TokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
