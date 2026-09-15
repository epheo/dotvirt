package model

import "testing"

// Every consumer derives from the table, so a kind reachable by name must map
// back to exactly the resource that lists it, and no two rows may claim one.
func TestKindTableRoundTrips(t *testing.T) {
	seenKind := map[string]string{}
	seenName := map[string]bool{}
	for _, r := range Resources() {
		if r.Name == "" || seenName[r.Name] {
			t.Errorf("resource %q: empty or duplicate name", r.Name)
		}
		seenName[r.Name] = true
		if r.CreateLabel == "" || r.EditLabel == "" {
			t.Errorf("resource %q: missing a Changes-pane label", r.Name)
		}
		got, ok := LookupResource(r.Name)
		if !ok || got.Name != r.Name {
			t.Errorf("LookupResource(%q) = %+v, %v", r.Name, got, ok)
		}
		for _, k := range r.Kinds {
			if k.Kind == "" || k.Version == "" || k.Plural == "" {
				t.Errorf("%s/%s: incomplete API coordinates %+v", r.Name, k.Kind, k)
			}
			if prev, dup := seenKind[k.Kind]; dup {
				t.Errorf("kind %s listed under both %s and %s", k.Kind, prev, r.Name)
			}
			seenKind[k.Kind] = r.Name
			back, bk, ok := LookupKind(k.Kind)
			if !ok || back.Name != r.Name || bk != k {
				t.Errorf("LookupKind(%s) = %s/%+v, %v; want %s", k.Kind, back.Name, bk, ok, r.Name)
			}
		}
	}
	if r, ok := LookupResource(""); !ok || r.Name != "vm" {
		t.Errorf("the empty resource must read as the VM, got %+v %v", r, ok)
	}
	if _, _, ok := LookupKind("DataVolume"); ok {
		t.Error("an unmanaged kind must not resolve")
	}
}

func TestKindAPIVersion(t *testing.T) {
	if got := KindNS.APIVersion(); got != "v1" {
		t.Errorf("core group: got %q", got)
	}
	if got := KindUDN.APIVersion(); got != "k8s.ovn.org/v1" {
		t.Errorf("grouped: got %q", got)
	}
}

func TestClusterScopeTranslation(t *testing.T) {
	for _, ns := range []string{"", "alpha", ClusterScopeNS} {
		if got := ObjectNamespace(DraftNamespace(ns)); got != ObjectNamespace(ns) {
			t.Errorf("%q: draft -> object gave %q", ns, got)
		}
	}
	if DraftNamespace("") != ClusterScopeNS || ObjectNamespace(ClusterScopeNS) != "" {
		t.Error("the sentinel must translate both ways")
	}
}

// The API groups the drift plane keys on; the NAD group in particular is easy
// to get wrong (k8s.cni.cncf.io, not k8s.ovn.org).
func TestKindGroups(t *testing.T) {
	cases := map[string]string{
		"UserDefinedNetwork":             "k8s.ovn.org",
		"ClusterUserDefinedNetwork":      "k8s.ovn.org",
		"NetworkAttachmentDefinition":    "k8s.cni.cncf.io",
		"NetworkPolicy":                  "networking.k8s.io",
		"AdminNetworkPolicy":             "policy.networking.k8s.io",
		"BaselineAdminNetworkPolicy":     "policy.networking.k8s.io",
		"EgressFirewall":                 "k8s.ovn.org",
		"EgressIP":                       "k8s.ovn.org",
		"AdminPolicyBasedExternalRoute":  "k8s.ovn.org",
		"NodeNetworkConfigurationPolicy": "nmstate.io",
		"Namespace":                      "",
	}
	for kind, want := range cases {
		_, k, ok := LookupKind(kind)
		if !ok || k.Group != want {
			t.Errorf("%s: group %q (found %v), want %q", kind, k.Group, ok, want)
		}
	}
}
