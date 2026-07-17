package api

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// scopePolicies must keep namespace-tier rows only in visible namespaces and
// cluster-tier rows only for callers with authority over that specific kind —
// an admin policy's rules name namespaces across tenants.
func TestScopePolicies(t *testing.T) {
	all := []model.Policy{
		{Name: "mine", Kind: model.PolicyDFW, Namespace: "team-a"},
		{Name: "theirs", Kind: model.PolicyDFW, Namespace: "team-b"},
		{Name: "block-dev", Kind: model.PolicyAdmin},
		{Name: "prod-egress", Kind: model.PolicyEgressIP},
	}
	visible := map[string]bool{"team-a": true}

	got := scopePolicies(all, visible, func(k model.PolicyKind) bool { return k == model.PolicyEgressIP })
	names := map[string]bool{}
	for _, p := range got {
		names[p.Name] = true
	}
	if !names["mine"] || !names["prod-egress"] {
		t.Errorf("visible rows missing: %v", names)
	}
	if names["theirs"] {
		t.Error("a foreign namespace's policy leaked")
	}
	if names["block-dev"] {
		t.Error("a cluster-tier kind leaked without authority over it")
	}
}
