package api

import (
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Zero project namespaces must warn only when the platform Application is
// actually broken: a pristine install (healthy app, nothing to apply) and a
// first sync in flight are silent, a missing or failing app names the breakage.
func TestPlatformSyncWarning(t *testing.T) {
	const repo = "https://forge.example/dotvirt/platform.git"
	key := forge.NormalizeRepoURL(repo)
	cases := []struct {
		name  string
		drift map[string]model.ProjectSync
		want  string // "" = silent; otherwise a substring of the warning
	}{
		{"argo off or pre-sync", nil, ""},
		{"healthy empty install", map[string]model.ProjectSync{key: {Health: "Healthy"}}, ""},
		{"first sync in flight", map[string]model.ProjectSync{key: {Health: "Progressing", Operation: "Running"}}, ""},
		{"app missing", map[string]model.ProjectSync{}, "dotvirt-platform Application is missing"},
		{"degraded", map[string]model.ProjectSync{key: {Health: "Degraded"}}, "sync is unhealthy"},
		{"failed op with cause", map[string]model.ProjectSync{key: {Health: "Healthy", Operation: "Failed", SyncError: "authentication required"}}, "authentication required"},
		{"comparison error", map[string]model.ProjectSync{key: {Health: "Unknown", SyncError: "repository not found"}}, "repository not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := platformSyncWarning(c.drift, repo)
			if c.want == "" && got != "" {
				t.Fatalf("want silence, got %q", got)
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Fatalf("want warning containing %q, got %q", c.want, got)
			}
		})
	}
}
