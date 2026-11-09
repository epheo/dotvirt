package tenancy

import (
	"slices"
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
		// The AppProject's destinationServiceAccounts name the account by this rule.
		if sa, saOK := SyncAccount(project); sa != name || saOK != got {
			t.Errorf("%q: SyncAccount = %q %v, want the AppProject's name", project, sa, saOK)
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

// A tenant repo never declares a NetworkAttachmentDefinition: the project's
// own admin role cannot create one, and Argo would apply it with its rights.
func TestTenantKindsExcludeNAD(t *testing.T) {
	for _, k := range TenantKinds {
		if k.Kind == "NetworkAttachmentDefinition" {
			t.Fatal("NetworkAttachmentDefinition is platform tier")
		}
	}
	if TenantKind("NetworkAttachmentDefinition") || !TenantKind("VirtualMachine") {
		t.Error("TenantKind disagrees with TenantKinds")
	}
}

type gk struct{ group, kind string }

func gks(kinds []Kind) []gk {
	out := make([]gk, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, gk{k.Group, k.Kind})
	}
	return out
}

// A change here widens what every tenant, or the platform repo, may apply:
// it is meant to be a deliberate edit, reviewed as a security change.
func TestWhatEachRepoMayDeclare(t *testing.T) {
	tenant := []gk{
		{"kubevirt.io", "VirtualMachine"},
		{"cdi.kubevirt.io", "DataVolume"},
		{"k8s.ovn.org", "UserDefinedNetwork"},
		{"k8s.ovn.org", "EgressFirewall"},
		{"networking.k8s.io", "NetworkPolicy"},
	}
	platformCluster := []gk{
		{"", "Namespace"},
		{"k8s.ovn.org", "ClusterUserDefinedNetwork"},
		{"k8s.ovn.org", "EgressIP"},
		{"k8s.ovn.org", "AdminPolicyBasedExternalRoute"},
		{"policy.networking.k8s.io", "AdminNetworkPolicy"},
		{"policy.networking.k8s.io", "BaselineAdminNetworkPolicy"},
		{"nmstate.io", "NodeNetworkConfigurationPolicy"},
		{"machineconfiguration.openshift.io", "MachineConfig"},
	}
	platformNamespaced := []gk{
		{"k8s.ovn.org", "UserDefinedNetwork"},
		{"k8s.cni.cncf.io", "NetworkAttachmentDefinition"},
		{"rbac.authorization.k8s.io", "RoleBinding"},
		{"operators.coreos.com", "Subscription"},
		{"operators.coreos.com", "OperatorGroup"},
		{"operator.openshift.io", "KubeDescheduler"},
	}
	for name, c := range map[string]struct{ got, want []gk }{
		"tenant":              {gks(TenantKinds), tenant},
		"platform cluster":    {gks(PlatformKinds(true)), platformCluster},
		"platform namespaced": {gks(PlatformKinds(false)), platformNamespaced},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s = %v, want %v", name, c.got, c.want)
		}
	}
}

func TestTenantKindsAreNamespaced(t *testing.T) {
	for _, k := range TenantKinds {
		if k.ClusterScoped {
			t.Errorf("%s is cluster-scoped: a tenant repo would reach outside its namespaces", k.Kind)
		}
	}
}
