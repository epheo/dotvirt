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

// TestRedactSubjects pins the effective/trace disclosure rule: the enumerated
// subject namespaces of a cluster-tier policy are authoring-tier information,
// stripped without authority over the kind — while the binding itself (and a
// namespace-tier policy) is untouched.
func TestRedactSubjects(t *testing.T) {
	none := func(model.PolicyKind) bool { return false }

	anp := model.Policy{Name: "lockdown", Kind: model.PolicyAdmin, Namespaces: []string{"tenant-a", "tenant-b"}}
	redactSubjects(none, &anp)
	if anp.Namespaces != nil {
		t.Errorf("admin subject namespaces survived without authority: %v", anp.Namespaces)
	}
	if anp.Name != "lockdown" {
		t.Error("the binding itself must stay")
	}

	dfw := model.Policy{Name: "allow-web", Kind: model.PolicyDFW, Namespace: "team-a", Namespaces: []string{"team-a"}}
	redactSubjects(none, &dfw)
	if dfw.Namespaces == nil {
		t.Error("namespace-tier policies are not admin-tier: must keep their namespaces")
	}

	allowed := model.Policy{Name: "lockdown", Kind: model.PolicyAdmin, Namespaces: []string{"tenant-a"}}
	redactSubjects(func(model.PolicyKind) bool { return true }, &allowed)
	if allowed.Namespaces == nil {
		t.Error("authority over the kind keeps the enumeration")
	}
}
