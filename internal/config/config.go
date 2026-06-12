// Package config loads dotvirt runtime configuration from flags and environment.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"time"
)

// Config holds everything dotvirt needs. Per-project git repos come from cluster
// annotations, not config — the only git input here is the one credential used to
// clone/push every project repo.
type Config struct {
	Addr     string // HTTP listen address
	UIOrigin string // CORS origin for the separate SvelteKit frontend; empty disables CORS

	// Git credential (one cred for every project repo) + the branches dotvirt uses.
	GitUsername   string // for https auth (token in GitToken)
	GitToken      string
	RunningBranch string // per-project branch dotvirt owns and writes cluster state to

	// Cluster read
	Kubeconfig     string // path; empty = in-cluster
	ProjectLabel   string // namespace label whose value names the project (dotvirt.io/project)
	RepoAnnotation string // namespace annotation holding the project's git repo URL (dotvirt.io/repo)

	ExportInterval  time.Duration // how often to export live state to each project's running branch
	GitPollInterval time.Duration // how often to poll git for branch changes (drives live push)
	Push            bool          // push commits to the remote (disable for local/offline testing)

	// Changeset / PR workflow
	BaseBranch     string // branch the inventory reads + PRs target (the GitOps trunk)
	ProposedBranch string // dotvirt-owned working branch holding a draft
	DraftDir       string // root dir for persisted drafts (<dir>/<user>/<project>.json)

	// Forge (Forgejo) for PR creation; empty url/token degrades to push-only. The
	// per-project owner/repo are derived from each project's repo URL, not config.
	ForgeURL   string
	ForgeToken string

	InsecureTLS bool // skip TLS verification for git + forge (dev, e.g. self-signed Route)

	// MetricsURL is the Prometheus/Thanos query API base URL backing the per-VM
	// Performance tab; empty disables the tab. InsecureTLS also covers this client.
	MetricsURL string

	// Auth
	SessionSecret string // HMAC key signing the session cookie; random if empty

	ArgoEnabled bool // enable ArgoCD drift reads + re-sync
}

// Load builds a Config from flags, with env-var fallbacks for secrets.
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("dotvirt", flag.ContinueOnError)
	c := &Config{}

	fs.StringVar(&c.Addr, "addr", envOr("DOTVIRT_ADDR", ":8080"), "HTTP listen address")
	fs.StringVar(&c.UIOrigin, "ui-origin", envOr("DOTVIRT_UI_ORIGIN", "http://localhost:5173"), "frontend origin allowed via CORS (empty to disable)")
	fs.StringVar(&c.GitUsername, "git-username", envOr("DOTVIRT_GIT_USERNAME", "dotvirt"), "git https username (clones/pushes every project repo)")
	fs.StringVar(&c.GitToken, "git-token", os.Getenv("DOTVIRT_GIT_TOKEN"), "git https token/password")
	fs.StringVar(&c.RunningBranch, "running-branch", envOr("DOTVIRT_RUNNING_BRANCH", "running"), "per-project branch reflecting live cluster state (dotvirt-owned)")

	fs.StringVar(&c.Kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig path (empty = in-cluster)")
	fs.StringVar(&c.ProjectLabel, "project-label", envOr("DOTVIRT_PROJECT_LABEL", "dotvirt.io/project"), "namespace label whose value names the project")
	fs.StringVar(&c.RepoAnnotation, "repo-annotation", envOr("DOTVIRT_REPO_ANNOTATION", "dotvirt.io/repo"), "namespace annotation holding the project's git repo URL")

	fs.DurationVar(&c.ExportInterval, "export-interval", 30*time.Second, "how often to export live state to each project's running branch")
	fs.DurationVar(&c.GitPollInterval, "git-poll-interval", 10*time.Second, "how often to poll git for branch changes (drives live inventory push)")
	fs.BoolVar(&c.Push, "push", envBool("DOTVIRT_PUSH", true), "push commits to the remote (disable for local/offline testing)")

	fs.StringVar(&c.BaseBranch, "base-branch", envOr("DOTVIRT_BASE_BRANCH", "main"), "branch the inventory reads + PRs target")
	fs.StringVar(&c.ProposedBranch, "proposed-branch", envOr("DOTVIRT_PROPOSED_BRANCH", "dotvirt/proposed"), "working branch holding the draft changeset")
	fs.StringVar(&c.DraftDir, "draft-dir", envOr("DOTVIRT_DRAFT_DIR", "./.dotvirt-drafts"), "root dir for persisted drafts (<dir>/<user>/<project>.json)")
	fs.StringVar(&c.ForgeURL, "forge-url", os.Getenv("DOTVIRT_FORGE_URL"), "Forgejo base URL (empty = push-only, no PR)")
	fs.StringVar(&c.ForgeToken, "forge-token", os.Getenv("DOTVIRT_FORGE_TOKEN"), "Forgejo API token")
	fs.BoolVar(&c.InsecureTLS, "insecure-tls", envBool("DOTVIRT_INSECURE_TLS", false), "skip TLS verification for git+forge (dev only)")
	fs.StringVar(&c.MetricsURL, "metrics-url", os.Getenv("DOTVIRT_METRICS_URL"), "Prometheus/Thanos query API base URL for the Performance tab (empty disables)")
	fs.StringVar(&c.SessionSecret, "session-secret", os.Getenv("DOTVIRT_SESSION_SECRET"), "HMAC key signing the session cookie (random if empty; sessions then drop on restart)")

	fs.BoolVar(&c.ArgoEnabled, "argo", envBool("DOTVIRT_ARGO", false), "enable ArgoCD drift reads")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if c.SessionSecret == "" {
		secret, err := randomSecret()
		if err != nil {
			return nil, err
		}
		c.SessionSecret = secret
		log.Println("config: no -session-secret set; using a random key — sessions won't survive a restart")
	}
	return c, nil
}

// randomSecret returns a 32-byte hex key for signing session cookies.
func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch os.Getenv(key) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	default:
		return def
	}
}
