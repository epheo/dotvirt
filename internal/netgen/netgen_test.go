package netgen

import (
	"strings"
	"testing"
)

func TestManifest(t *testing.T) {
	path, content, err := Manifest(Spec{Name: "db-net", Namespace: "tenant-a", Scope: "project", Subnets: []string{"10.20.0.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "tenant-a/networks/db-net.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	for _, want := range []string{
		"kind: UserDefinedNetwork",
		"name: db-net",
		"namespace: tenant-a",
		"topology: Layer2",
		"role: Secondary",
		"10.20.0.0/24",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("manifest missing %q:\n%s", want, y)
		}
	}
}

func TestManifestNoSubnets(t *testing.T) {
	_, content, err := Manifest(Spec{Name: "iso", Namespace: "tenant-a"})
	if err != nil {
		t.Fatal(err)
	}
	y := string(content)
	if strings.Contains(y, "subnets") {
		t.Errorf("expected no subnets key when none given:\n%s", y)
	}
	// A subnet-less Secondary network must disable IPAM explicitly (else OVN-K
	// defaults ipam to Enabled and rejects it for missing subnets).
	if !strings.Contains(y, "mode: Disabled") {
		t.Errorf("expected ipam mode Disabled when no subnets:\n%s", y)
	}
}

func TestManifestValidate(t *testing.T) {
	if _, _, err := Manifest(Spec{Name: "x"}); err == nil {
		t.Error("expected error when namespace is empty")
	}
	if _, _, err := Manifest(Spec{Name: "x", Namespace: "n", Scope: "bogus"}); err == nil {
		t.Error("expected error for unsupported scope")
	}
}

func TestVLANManifest(t *testing.T) {
	path, content, err := Manifest(Spec{
		Name: "prod-vlan-200", Scope: ScopeVLAN, VLAN: 200,
		PhysicalNetwork: "physnet-prod", Namespaces: []string{"tenant-a", "tenant-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "networks/prod-vlan-200.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	for _, want := range []string{
		"kind: ClusterUserDefinedNetwork",
		"topology: Localnet",
		"physicalNetworkName: physnet-prod",
		"id: 200",
		"kubernetes.io/metadata.name",
		"tenant-a",
		"tenant-b",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("CUDN missing %q:\n%s", want, y)
		}
	}
}

func TestVLANValidate(t *testing.T) {
	base := Spec{Name: "v", Scope: ScopeVLAN, VLAN: 10, PhysicalNetwork: "p", Namespaces: []string{"n"}}
	ok := base
	if _, _, err := Manifest(ok); err != nil {
		t.Fatalf("valid vlan spec errored: %v", err)
	}
	bad := base
	bad.PhysicalNetwork = ""
	if _, _, err := Manifest(bad); err == nil {
		t.Error("expected error without an uplink")
	}
	bad = base
	bad.VLAN = 0
	if _, _, err := Manifest(bad); err == nil {
		t.Error("expected error without a VLAN id")
	}
	bad = base
	bad.Namespaces = nil
	if _, _, err := Manifest(bad); err == nil {
		t.Error("expected error without namespaces")
	}
}

func TestNamespaceManifestWithVMNetwork(t *testing.T) {
	path, content, err := NamespaceManifest(NamespaceSpec{
		Name: "tenant-c", Project: "team-c", Repo: "https://forge/dotvirt/team-c.git",
		VMNetwork: &PrimaryNet{Name: "tenant-c-net", Subnet: "10.40.0.0/16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "namespaces/tenant-c.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	for _, want := range []string{
		"kind: Namespace",
		"dotvirt.io/project: team-c",
		"k8s.ovn.org/primary-user-defined-network", // namespace label for primary UDN
		"dotvirt.io/repo:",
		"---", // multi-doc: namespace + UDN
		"kind: UserDefinedNetwork",
		"name: tenant-c-net",
		"namespace: tenant-c",
		"role: Primary",
		"10.40.0.0/16",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("namespace manifest missing %q:\n%s", want, y)
		}
	}
}

func TestNamespaceManifestNoNetwork(t *testing.T) {
	_, content, err := NamespaceManifest(NamespaceSpec{Name: "tenant-d", Project: "team-d"})
	if err != nil {
		t.Fatal(err)
	}
	y := string(content)
	if strings.Contains(y, "UserDefinedNetwork") || strings.Contains(y, "primary-user-defined-network") {
		t.Errorf("expected no UDN/primary label without a VM Network:\n%s", y)
	}
}

func TestNamespaceManifestVMNetworkIsLayer2(t *testing.T) {
	// A VM Network is always a primary Layer2 UDN.
	_, content, err := NamespaceManifest(NamespaceSpec{
		Name: "x", Project: "p", VMNetwork: &PrimaryNet{Name: "n", Subnet: "10.50.0.0/16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	y := string(content)
	if !strings.Contains(y, "topology: Layer2") || strings.Contains(y, "Layer3") {
		t.Errorf("VM Network should be Layer2 only:\n%s", y)
	}
}

func TestNamespaceManifestVMNetworkRequiresSubnet(t *testing.T) {
	// A primary UDN must do IPAM, so a subnet-less VM Network is rejected (OVN-K
	// won't accept a subnet-less or ipam-disabled primary network).
	if _, _, err := NamespaceManifest(NamespaceSpec{
		Name: "x", Project: "p", VMNetwork: &PrimaryNet{Name: "n"},
	}); err == nil {
		t.Error("expected error for a VM Network without a subnet")
	}
}

func TestUplinkManifest(t *testing.T) {
	path, content, err := UplinkManifest(UplinkSpec{Name: "physnet-prod", NIC: "eno2"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "uplinks/physnet-prod.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	for _, want := range []string{
		"kind: NodeNetworkConfigurationPolicy",
		"type: ovs-bridge",
		"name: br-physnet-prod", // default bridge
		"name: eno2",
		"localnet: physnet-prod",
		"node-role.kubernetes.io/worker", // default node selector
	} {
		if !strings.Contains(y, want) {
			t.Errorf("NNCP missing %q:\n%s", want, y)
		}
	}
}

func TestSharedCUDN(t *testing.T) {
	// An isolated shared network is a Layer2 (not localnet) secondary CUDN spanning
	// the selected namespaces — no uplink, no VLAN.
	path, content, err := Manifest(Spec{Name: "db-shared", Scope: ScopeShared, Namespaces: []string{"tenant-a", "tenant-b"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "networks/db-shared.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	for _, want := range []string{
		"kind: ClusterUserDefinedNetwork",
		"topology: Layer2",
		"role: Secondary",
		"mode: Disabled", // no subnet → IPAM disabled
		"kubernetes.io/metadata.name",
		"tenant-a",
		"tenant-b",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("shared CUDN missing %q:\n%s", want, y)
		}
	}
	if strings.Contains(y, "Localnet") || strings.Contains(y, "vlan") {
		t.Errorf("an isolated shared network must not be localnet/VLAN:\n%s", y)
	}
	if _, _, err := Manifest(Spec{Name: "x", Scope: ScopeShared}); err == nil {
		t.Error("expected error when no namespaces are selected")
	}
}
