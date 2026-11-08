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

// TestTenantPlatformView pins LIVE-3: a caller without platform authority
// keeps the platform verdict but learns no other tenant's names - unhealthy
// objects outside their namespaces and the apply error text are withheld.
func TestTenantPlatformView(t *testing.T) {
	if tenantPlatformView(nil, nil) != nil {
		t.Fatal("no rollup stays no rollup")
	}
	full := &model.ProjectSync{
		Sync: model.SyncOutOfSync, Health: "Degraded", Operation: "Failed", Revision: "abc1234",
		SyncError: `namespaces "team-b" is forbidden`,
		Unhealthy: []model.ObjectHealth{
			{Kind: "RoleBinding", Namespace: "team-a", Name: "edit", Health: "Degraded"},
			{Kind: "RoleBinding", Namespace: "team-b", Name: "edit", Health: "Degraded"},
			{Kind: "Namespace", Name: "team-a", Health: "Missing"},
			{Kind: "Namespace", Name: "team-b", Health: "Missing"},
			{Kind: "NodeNetworkConfigurationPolicy", Name: "dc-vlan-bridge", Health: "Degraded"},
			{Kind: "AdminNetworkPolicy", Name: "acme-lockdown", Message: `AdminNetworkPolicy "acme-lockdown" is invalid`},
		},
	}
	got := tenantPlatformView(full, map[string]bool{"team-a": true})
	if got.Sync != full.Sync || got.Health != full.Health || got.Operation != full.Operation {
		t.Errorf("the verdict must stay: %+v", got)
	}
	if got.SyncError == "" || strings.Contains(got.SyncError, "team-b") || got.Revision != "" {
		t.Errorf("apply error = %q, revision = %q; want a generic line and no detail", got.SyncError, got.Revision)
	}
	want := []model.ObjectHealth{full.Unhealthy[0], full.Unhealthy[2]}
	if len(got.Unhealthy) != len(want) || got.Unhealthy[0] != want[0] || got.Unhealthy[1] != want[1] {
		t.Errorf("unhealthy = %+v, want only team-a's objects", got.Unhealthy)
	}
	if len(full.Unhealthy) != 6 {
		t.Error("the shared rollup was mutated")
	}
}
