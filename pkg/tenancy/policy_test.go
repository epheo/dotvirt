package tenancy

import (
	"slices"
	"testing"
)

// Listed decides by name, as before. Any admits a namespaced kind dotvirt has
// no row for, and refuses grants, OLM, a cluster-scoped kind and whatever the
// provider adds.
func TestPolicyDeclarable(t *testing.T) {
	listed := Policy{}
	if !listed.Declarable("kubevirt.io", "VirtualMachine", false) || listed.Declarable("apps", "Deployment", false) {
		t.Error("Listed: a VirtualMachine and only listed kinds")
	}
	anyKinds := AnyPolicy(GroupKind{"capsule.clastix.io", "*"})
	for _, c := range []struct {
		group, kind   string
		clusterScoped bool
		want          bool
	}{
		{"apps", "Deployment", false, true},
		{"kubevirt.io", "VirtualMachine", false, true},
		{"rbac.authorization.k8s.io", "Role", false, false},
		{"operators.coreos.com", "Subscription", false, false},
		{"operators.coreos.com", "CatalogSource", false, false},
		{"capsule.clastix.io", "Tenant", false, false},
		{"scheduling.k8s.io", "PriorityClass", true, false},
	} {
		if got := anyKinds.Declarable(c.group, c.kind, c.clusterScoped); got != c.want {
			t.Errorf("Any: %s/%s = %v, want %v", c.group, c.kind, got, c.want)
		}
	}
}

func TestGroupKindsRoundTrip(t *testing.T) {
	in := []GroupKind{{"capsule.clastix.io", "*"}, {"", "Secret"}, {"apps", "DaemonSet"}}
	s := FormatGroupKinds(in)
	if s != "capsule.clastix.io/*,/Secret,apps/DaemonSet" {
		t.Errorf("format = %q", s)
	}
	out, err := ParseGroupKinds(s)
	if err != nil || !slices.Equal(out, in) {
		t.Errorf("parse = %v, %v", out, err)
	}
	if got, err := ParseGroupKinds(""); err != nil || got != nil {
		t.Errorf("empty = %v, %v", got, err)
	}
	for _, bad := range []string{"Deployment", "apps/", "a/b/c"} {
		if _, err := ParseGroupKinds(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
