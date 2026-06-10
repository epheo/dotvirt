// Package config loads dotvirt runtime configuration from flags and environment.
package config

import (
	"flag"
	"fmt"
	"os"
	"time"
)

// Config holds everything dotvirt needs to connect its three read sources
// (git, cluster, ArgoCD) and its one write target (git feature branches).
type Config struct {
	Addr     string // HTTP listen address
	UIOrigin string // CORS origin for the separate SvelteKit frontend; empty disables CORS

	// Git plane
	RepoURL       string // remote URL, or local path for a working copy
	RepoCacheDir  string // where dotvirt keeps its working clone
	RunningBranch string // branch dotvirt owns and writes cluster state to
	ManifestGlob  string // glob (relative to repo root) selecting VM manifests
	GitUsername   string // for https auth (token in GitToken)
	GitToken      string

	// Cluster read
	Kubeconfig     string // path; empty = in-cluster
	NamespaceLabel string // label selector defining "projects"

	ExportInterval  time.Duration // how often to export live state to the running branch
	GitPollInterval time.Duration // how often to poll git for branch changes (drives live push)
	Push            bool          // push commits to the remote (disable for local/offline testing)

	// Toggles for environments where a piece isn't wired yet.
	ClusterEnabled bool
	ArgoEnabled    bool
}

// Load builds a Config from flags, with env-var fallbacks for secrets.
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("dotvirt", flag.ContinueOnError)
	c := &Config{}

	fs.StringVar(&c.Addr, "addr", envOr("DOTVIRT_ADDR", ":8080"), "HTTP listen address")
	fs.StringVar(&c.UIOrigin, "ui-origin", envOr("DOTVIRT_UI_ORIGIN", "http://localhost:5173"), "frontend origin allowed via CORS (empty to disable)")
	fs.StringVar(&c.RepoURL, "repo", os.Getenv("DOTVIRT_REPO"), "git repo URL or local path")
	fs.StringVar(&c.RepoCacheDir, "repo-cache", envOr("DOTVIRT_REPO_CACHE", "./.dotvirt-repo"), "local working clone dir")
	fs.StringVar(&c.RunningBranch, "running-branch", envOr("DOTVIRT_RUNNING_BRANCH", "running"), "branch reflecting live cluster state (dotvirt-owned)")
	fs.StringVar(&c.ManifestGlob, "glob", envOr("DOTVIRT_GLOB", "**/*.yaml"), "glob selecting VM manifests")
	fs.StringVar(&c.GitUsername, "git-username", envOr("DOTVIRT_GIT_USERNAME", "dotvirt"), "git https username")
	fs.StringVar(&c.GitToken, "git-token", os.Getenv("DOTVIRT_GIT_TOKEN"), "git https token/password")

	fs.StringVar(&c.Kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig path (empty = in-cluster)")
	fs.StringVar(&c.NamespaceLabel, "namespace-label", envOr("DOTVIRT_NAMESPACE_LABEL", "dotvirt.io/project"), "label selector for project namespaces")

	fs.DurationVar(&c.ExportInterval, "export-interval", 30*time.Second, "how often to export live state to the running branch")
	fs.DurationVar(&c.GitPollInterval, "git-poll-interval", 10*time.Second, "how often to poll git for branch changes (drives live inventory push)")
	fs.BoolVar(&c.Push, "push", envBool("DOTVIRT_PUSH", true), "push commits to the remote (disable for local/offline testing)")
	fs.BoolVar(&c.ClusterEnabled, "cluster", envBool("DOTVIRT_CLUSTER", false), "enable live cluster reads + running-branch export")
	fs.BoolVar(&c.ArgoEnabled, "argo", envBool("DOTVIRT_ARGO", false), "enable ArgoCD drift reads")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if c.RepoURL == "" {
		return nil, fmt.Errorf("repo is required (-repo or DOTVIRT_REPO)")
	}
	return c, nil
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
