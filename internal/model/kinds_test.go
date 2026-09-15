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
	if got := MustKind("Namespace").APIVersion(); got != "v1" {
		t.Errorf("core group: got %q", got)
	}
	if got := MustKind("UserDefinedNetwork").APIVersion(); got != "k8s.ovn.org/v1" {
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
