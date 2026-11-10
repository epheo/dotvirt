package tenancy

import (
	"slices"
	"testing"
)

// A policy allows exactly the kinds it holds: none in the zero value.
func TestPolicyAllows(t *testing.T) {
	if (Policy{}).Allows("apps", "Deployment") {
		t.Error("the zero policy allows nothing")
	}
	p := Policy{Allowed: []GroupKind{{"apps", "Deployment"}}}
	if !p.Allows("apps", "Deployment") || p.Allows("apps", "StatefulSet") || p.Allows("example.com", "Deployment") {
		t.Error("allows the kinds it holds, matching group and kind")
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
