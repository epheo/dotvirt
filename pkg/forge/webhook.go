// Webhook reconciliation: converging one push+pull_request hook per repo or
// org, with a secret-fingerprint cache to keep steady-state sweeps write-free.

package forge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
)

// hook is a repo webhook as returned by Forgejo (subset).
type hook struct {
	ID     int               `json:"id"`
	Config map[string]string `json:"config"`
}

// EnsureWebhook registers a push+pull_request webhook on the client's REPO delivering
// to targetURL (HMAC-signed with secret). Idempotent and safe on every sweep: it
// migrates the hook in place when targetURL's host changes and re-asserts the secret at
// most once per process. See ensureHook.
func (c *Client) EnsureWebhook(targetURL, secret string) error {
	return c.ensureHook(c.repoPath("/hooks"), targetURL, secret)
}

// EnsureOrgWebhook registers the same push+pull_request webhook on the client's
// ORGANIZATION rather than a single repo, so one hook covers every repo in the org —
// present and future. Used to point ArgoCD at all project repos with a single
// registration, with no per-repo enumeration. Same reconcile semantics as
// EnsureWebhook (see ensureHook).
func (c *Client) EnsureOrgWebhook(targetURL, secret string) error {
	return c.ensureHook(fmt.Sprintf("/api/v1/orgs/%s/hooks", c.owner), targetURL, secret)
}

// ensureHook converges a single "gitea" (Forgejo-compatible) push+pull_request webhook
// delivering to targetURL within the given hooks collection (repo- or org-level).
//
// The hook is identified by its URL PATH, not its full URL. The host legitimately
// changes — an external Route giving way to the in-cluster Service — and matching on the
// path migrates that one hook in place rather than orphaning the old hook and POSTing a
// duplicate that double-delivers. Extra hooks sharing the path (from an earlier
// migration or a manual add) are deleted, so deliveries never split across a
// half-configured second hook.
//
// Forgejo never echoes a hook's stored secret, so a converged hook is indistinguishable
// from one carrying a stale/rotated secret that 403s every delivery. The fingerprint
// cache (hookSecrets) records the secret last written per hook, so the secret is
// re-asserted at most once per process — on first sight or after a rotation — not on
// every sweep. That keeps steady-state sweeps write-free against Forgejo's single-replica
// sqlite, and leaves a converged hook exactly as the forge has it (active or not) instead
// of fighting its failure-driven auto-disable.
func (c *Client) ensureHook(hooksPath, targetURL, secret string) error {
	var hooks []hook
	if err := c.do("GET", hooksPath, nil, &hooks); err != nil {
		return err
	}
	cfg := map[string]string{"url": targetURL, "content_type": "json", "secret": secret}
	// One desired payload; the create API additionally requires "type" (the edit API
	// rejects it), added on the POST branch only.
	desired := map[string]any{"active": true, "events": []string{"push", "pull_request"}, "config": cfg}

	targetPath := urlPath(targetURL)
	var ours []hook
	for _, h := range hooks {
		if urlPath(h.Config["url"]) == targetPath {
			ours = append(ours, h)
		}
	}
	if len(ours) == 0 {
		return c.do("POST", hooksPath, withCreateType(desired), nil)
	}

	// Reconcile the first match in place; PATCH only on a real change — a host migration
	// or a secret the cache hasn't seen this process — so a re-enable (active:true) and a
	// secret rewrite happen exactly when they recover something, not every sweep. Record
	// only after the write lands, or a failed PATCH would falsely mark the hook converged.
	primary := ours[0]
	key := fmt.Sprintf("%s#%d", hooksPath, primary.ID)
	if primary.Config["url"] != targetURL || !hookSecretMatches(key, secret) {
		if err := c.do("PATCH", fmt.Sprintf("%s/%d", hooksPath, primary.ID), desired, nil); err != nil {
			return err
		}
		recordHookSecret(key, secret)
	}
	for _, dup := range ours[1:] {
		if err := c.do("DELETE", fmt.Sprintf("%s/%d", hooksPath, dup.ID), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// hookSecrets fingerprints the secret last written to each reconciled hook, keyed by
// "{collection}#{id}". It exists because Forgejo never echoes a hook's stored secret:
// without it ensureHook could not tell a converged hook from one needing its secret
// re-asserted, and would PATCH on every sweep. Process-lifetime state — a restart
// re-asserts once, which is the intended recovery.
var (
	hookSecretsMu sync.Mutex
	hookSecrets   = map[string]string{}
)

func hookFingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// hookSecretMatches reports whether key was last reconciled with this secret.
func hookSecretMatches(key, secret string) bool {
	hookSecretsMu.Lock()
	defer hookSecretsMu.Unlock()
	return hookSecrets[key] == hookFingerprint(secret)
}

// recordHookSecret remembers the secret just written to key.
func recordHookSecret(key, secret string) {
	hookSecretsMu.Lock()
	defer hookSecretsMu.Unlock()
	hookSecrets[key] = hookFingerprint(secret)
}

// withCreateType copies a hook payload and sets the create-only "type" field.
func withCreateType(base map[string]any) map[string]any {
	out := make(map[string]any, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["type"] = "gitea"
	return out
}
