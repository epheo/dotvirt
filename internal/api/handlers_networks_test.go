package api

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// scopeNetworks must keep shared (cluster) networks discoverable to every tenant
// but never reveal a namespace the caller can't already see — neither a project
// network in a foreign namespace nor, on a shared network, the foreign entries of
// its own Namespaces list (a CUDN publishes cluster-wide, so that list can name
// other tenants' namespaces).
func TestScopeNetworks(t *testing.T) {
	visible := map[string]bool{"tenant-a": true}
	in := []model.Network{
		{Name: "vm-net", Scope: model.ScopeProject, Namespace: "tenant-a"},
		{Name: "secret", Scope: model.ScopeProject, Namespace: "tenant-b"},
		{Name: "prod-vlan", Scope: model.ScopeShared, Namespaces: []string{"tenant-a", "tenant-b", "tenant-c"}},
	}
	got := scopeNetworks(in, visible)

	byName := map[string]model.Network{}
	for _, n := range got {
		byName[n.Name] = n
	}
	if _, ok := byName["secret"]; ok {
		t.Error("a project network in a non-visible namespace leaked")
	}
	if _, ok := byName["vm-net"]; !ok {
		t.Error("a project network in the caller's namespace should be kept")
	}
	shared, ok := byName["prod-vlan"]
	if !ok {
		t.Fatal("a shared network should stay discoverable to every tenant")
	}
	if len(shared.Namespaces) != 1 || shared.Namespaces[0] != "tenant-a" {
		t.Errorf("shared network namespaces = %v, want [tenant-a] (foreign namespaces redacted)", shared.Namespaces)
	}
	// Per-request scoping must not mutate the shared, cached catalog.
	if len(in[2].Namespaces) != 3 {
		t.Errorf("scopeNetworks mutated the cached input slice: %v", in[2].Namespaces)
	}
}
