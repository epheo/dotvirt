package tenancy

import (
	"testing"
	"time"
)

func TestAppProjectName(t *testing.T) {
	for project, ok := range map[string]bool{
		"team-a":                  true,
		"a.b":                     true,
		PlatformProject:           false,
		"":                        false,
		"Team_A":                  false,
		"a_b":                     false,
		"a..b":                    false,
		string(make([]byte, 250)): false,
	} {
		name, got := AppProjectName(project)
		if got != ok {
			t.Errorf("%q: ok = %v, want %v", project, got, ok)
		}
		if name != AppProjectPrefix+project {
			t.Errorf("%q: name = %q", project, name)
		}
	}
}

func TestReserved(t *testing.T) {
	platform := []string{"dotvirt", "openshift-gitops"}
	for ns, want := range map[string]bool{
		"kube-system": true, "openshift-gitops": true, "openshift-x": true, "dotvirt": true,
		"default": false, "team-a": false, "kubevirt": false,
	} {
		if got := Reserved(ns, platform); got != want {
			t.Errorf("Reserved(%q) = %v, want %v", ns, got, want)
		}
	}
}

// The destination holds still as namespaces join: a newer namespace never
// displaces it, whatever its name, and platform space never qualifies.
func TestDestination(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	nss := []Namespace{{"team-prod", t0.Add(time.Hour)}, {"team-dev", t0}, {"kube-team", t0.Add(-time.Hour)}}
	if got, _ := Destination("team", nss, nil); got != "team-dev" {
		t.Fatalf("destination = %s, want the oldest non-reserved", got)
	}
	joined := append(nss, Namespace{"aaa", t0.Add(2 * time.Hour)})
	if got, _ := Destination("team", joined, nil); got != "team-dev" {
		t.Errorf("a newer namespace moved the destination to %s", got)
	}
	named := append(joined, Namespace{"team", t0.Add(3 * time.Hour)})
	if got, _ := Destination("team", named, nil); got != "team" {
		t.Errorf("destination = %s, want the namespace named after the project", got)
	}
	tie := []Namespace{{"b", t0}, {"a", t0}}
	if got, _ := Destination("team", tie, nil); got != "a" {
		t.Errorf("tie destination = %s, want name order", got)
	}
	if _, ok := Destination("team", []Namespace{{"kube-x", t0}, {"argo", t0}}, []string{"argo"}); ok {
		t.Error("a project in platform space only has no destination")
	}
}
