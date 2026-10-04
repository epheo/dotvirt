package api

import (
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/forge"
)

// The platform tier rides the frame as its Application's rollup: absent while
// Argo is off or pre-sync, the app's own state when it exists, and a standing
// error when nothing applies the platform repo.
func TestPlatformRollup(t *testing.T) {
	const repo = "https://forge.example/dotvirt/platform.git"
	key := forge.NormalizeRepoURL(repo)
	if got := platformRollup(nil, repo); got != nil {
		t.Fatalf("argo off or pre-sync must yield nil, got %+v", got)
	}
	degraded := model.ProjectSync{Health: "Degraded", Unhealthy: []model.ObjectHealth{{Kind: "NodeNetworkConfigurationPolicy", Name: "dc-vlan-bridge", Health: "Degraded"}}}
	got := platformRollup(map[string]model.ProjectSync{key: degraded}, repo)
	if got == nil || got.Health != "Degraded" || len(got.Unhealthy) != 1 || got.Unhealthy[0].Name != "dc-vlan-bridge" {
		t.Fatalf("want the app's own rollup with its unhealthy objects, got %+v", got)
	}
	missing := platformRollup(map[string]model.ProjectSync{}, repo)
	if missing == nil || missing.Operation != "Error" || !strings.Contains(missing.SyncError, "dotvirt-platform Application is missing") {
		t.Fatalf("an absent Application must read as a standing error, got %+v", missing)
	}
}
