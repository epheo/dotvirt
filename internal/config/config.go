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

	"github.com/epheo/dotvirt/pkg/forge"
)

// Config holds everything dotvirt needs. Per-project git repos come from cluster
// annotations, not config — the only git input here is the one credential used to
// clone/push every project repo.
type Config struct {
	Addr     string // HTTP listen address
	UIOrigin string // CORS origin for the separate SvelteKit frontend; empty disables CORS

	// Git credential (one cred for every project repo) + the branches dotvirt uses.
	// The git https token and the Forge API token are ONE credential (the forge bot):
	// GitToken is a fallback for ForgeToken; both resolve through ForgeTokenSource so
	// a rotated token is picked up without restart. See ForgeTokenFile / ForgeToken.
	GitUsername   string // for https auth (the token comes from ForgeTokenSource)
	GitToken      string // deprecated alias for ForgeToken (kept for BYO flag compat)
	RunningBranch string // per-project branch dotvirt owns and writes cluster state to

	// Cluster read
	Kubeconfig     string // path; empty = in-cluster
	ProjectLabel   string // namespace label whose value names the project (dotvirt.io/project)
	RepoAnnotation string // namespace annotation holding the project's git repo URL (dotvirt.io/repo)

	// PlatformRepo is the platform-tier git repo holding cluster-scoped + tenancy
	// manifests (Namespaces, CUDNs, NNCP uplinks, primary VM networks). dotvirt
	// routes every cluster-scoped create here by KIND (never a tenant repo) and
	// SSAR-gates it; empty disables those create flows. It is NOT a
	// dotvirt.io/project-labeled project — it's platform-provisioned (its own Argo
	// app + AppProject; see deploy/appprojects.yaml).
	PlatformRepo string

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
	// ForgeTokenFile, when set, is a mounted-secret path read on EVERY forge/git
	// call (see ForgeTokenSource) — kubelet updates it in place, so an operator
	// re-mint/rotation takes effect with no pod restart. Takes precedence over the
	// static ForgeToken/GitToken env values.
	ForgeTokenFile string

	InsecureTLS bool // skip TLS verification for git + forge (dev, e.g. self-signed Route)

	// MetricsURL is the Prometheus/Thanos query API base URL backing the per-VM
	// Performance tab; empty disables the tab. InsecureTLS also covers this client.
	MetricsURL string
	// MetricsCA is a PEM bundle to trust for MetricsURL — in-cluster, the mounted
	// service-CA that signs thanos-querier's serving cert (so no -insecure-tls).
	MetricsCA string

	// UploadProxyURL is the cdi-uploadproxy base the browser streams uploaded
	// images to (e.g. https://cdi-uploadproxy-…apps.example/); from
	// cdiconfig.status.uploadProxyURL. Empty disables the image-upload feature.
	UploadProxyURL string

	// Webhook: Forgejo pushes/PR events hit POST /api/webhooks/forge (HMAC-signed
	// with WebhookSecret; empty disables the endpoint). PublicURL is dotvirt's
	// externally reachable base (the Route). WebhookURL is the base the forge DELIVERS
	// to when registering the hook — distinct because an in-cluster forge typically
	// can't reach or TLS-trust the external Route, so it points at the in-cluster
	// Service (e.g. http://dotvirt.<ns>.svc:8080). Empty falls back to PublicURL;
	// auto-registration runs when either base and WebhookSecret are set.
	WebhookSecret string
	PublicURL     string
	WebhookURL    string

	// AppSetPluginToken is the shared bearer the ArgoCD ApplicationSet plugin
	// generator presents to dotvirt's /api/v1/getparams.execute endpoint, which
	// emits one {project,repo,namespace} element per labeled namespace so projects
	// provision dynamically from the dotvirt.io/project label. Empty disables it.
	AppSetPluginToken string

	// StaticDir is the built SPA directory the binary serves at the same origin (the
	// container packs it here). Empty in dev, where Vite serves the SPA on :5173.
	StaticDir string

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
	fs.StringVar(&c.PlatformRepo, "platform-repo", os.Getenv("DOTVIRT_PLATFORM_REPO"), "platform-tier git repo for cluster-scoped + tenancy manifests (CUDN/NNCP/Namespace); empty disables those creates")

	fs.DurationVar(&c.ExportInterval, "export-interval", 30*time.Second, "how often to export live state to each project's running branch")
	fs.DurationVar(&c.GitPollInterval, "git-poll-interval", 10*time.Second, "how often to poll git for branch changes (drives live inventory push)")
	fs.BoolVar(&c.Push, "push", envBool("DOTVIRT_PUSH", true), "push commits to the remote (disable for local/offline testing)")

	fs.StringVar(&c.BaseBranch, "base-branch", envOr("DOTVIRT_BASE_BRANCH", "main"), "branch the inventory reads + PRs target")
	fs.StringVar(&c.ProposedBranch, "proposed-branch", envOr("DOTVIRT_PROPOSED_BRANCH", "dotvirt/proposed"), "working branch holding the draft changeset")
	fs.StringVar(&c.DraftDir, "draft-dir", envOr("DOTVIRT_DRAFT_DIR", "./.dotvirt-drafts"), "root dir for persisted drafts (<dir>/<user>/<project>.json)")
	fs.StringVar(&c.ForgeURL, "forge-url", os.Getenv("DOTVIRT_FORGE_URL"), "Forgejo base URL (empty = push-only, no PR)")
	fs.StringVar(&c.ForgeToken, "forge-token", os.Getenv("DOTVIRT_FORGE_TOKEN"), "Forgejo API token (git https + API; falls back to -git-token)")
	fs.StringVar(&c.ForgeTokenFile, "forge-token-file", os.Getenv("DOTVIRT_FORGE_TOKEN_FILE"), "path to a mounted secret holding the forge token, re-read per call (rotation-safe; overrides -forge-token)")
	fs.BoolVar(&c.InsecureTLS, "insecure-tls", envBool("DOTVIRT_INSECURE_TLS", false), "skip TLS verification for git+forge (dev only)")
	fs.StringVar(&c.MetricsURL, "metrics-url", os.Getenv("DOTVIRT_METRICS_URL"), "Prometheus/Thanos query API base URL for the Performance tab (empty disables)")
	fs.StringVar(&c.MetricsCA, "metrics-ca", os.Getenv("DOTVIRT_METRICS_CA"), "PEM CA bundle path to trust for -metrics-url (e.g. the mounted service-CA)")
	fs.StringVar(&c.UploadProxyURL, "upload-proxy-url", os.Getenv("DOTVIRT_UPLOAD_PROXY_URL"), "cdi-uploadproxy base URL for image uploads (empty disables the feature)")
	fs.StringVar(&c.WebhookSecret, "webhook-secret", os.Getenv("DOTVIRT_WEBHOOK_SECRET"), "HMAC secret for the Forgejo webhook endpoint (empty disables it)")
	fs.StringVar(&c.PublicURL, "public-url", os.Getenv("DOTVIRT_PUBLIC_URL"), "dotvirt's externally reachable base URL, for webhook auto-registration (empty disables)")
	fs.StringVar(&c.WebhookURL, "webhook-url", os.Getenv("DOTVIRT_WEBHOOK_URL"), "base URL the forge delivers webhooks to (defaults to -public-url; set to the in-cluster Service URL when the forge can't reach/TLS-trust the external Route)")
	fs.StringVar(&c.AppSetPluginToken, "appset-plugin-token", os.Getenv("DOTVIRT_APPSET_PLUGIN_TOKEN"), "shared bearer for the ArgoCD ApplicationSet plugin-generator endpoint (empty disables it)")
	fs.StringVar(&c.StaticDir, "static-dir", os.Getenv("DOTVIRT_STATIC_DIR"), "directory of the built SPA to serve at the same origin (empty = dev: SPA served by Vite)")
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

// ForgeTokenSource is the single resolver for the forge credential, shared by the
// git RepoSet and the Forge API client so they never diverge. Prefers the mounted
// file (rotation-safe, re-read per call) and falls back to the static token —
// itself ForgeToken or, for BYO flag compatibility, GitToken.
func (c *Config) ForgeTokenSource() forge.TokenSource {
	if c.ForgeTokenFile != "" {
		return forge.FileToken(c.ForgeTokenFile)
	}
	tok := c.ForgeToken
	if tok == "" {
		tok = c.GitToken
	}
	return forge.StaticToken(tok)
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
