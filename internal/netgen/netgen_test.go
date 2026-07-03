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

func TestRoleBindingManifest(t *testing.T) {
	path, content, err := RoleBindingManifest(RoleBindingSpec{
		Namespace: "tenant-a", Project: "team-a", Owners: []string{"alice", "bob"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "rbac/tenant-a.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	for _, want := range []string{
		"kind: RoleBinding",
		"name: tenant-a-admins",
		"namespace: tenant-a",
		"dotvirt.io/project: team-a",
		"kind: ClusterRole",
		"name: admin", // default namespace-admin role
		"kind: User",
		"name: alice",
		"name: bob",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("RoleBinding missing %q:\n%s", want, y)
		}
	}
	// A custom role overrides the "admin" default.
	if _, c2, _ := RoleBindingManifest(RoleBindingSpec{Namespace: "ns", Owners: []string{"x"}, Role: "edit"}); !strings.Contains(string(c2), "name: edit") {
		t.Errorf("custom role not honored:\n%s", c2)
	}
	// Errors: a namespace and at least one owner are required.
	if _, _, err := RoleBindingManifest(RoleBindingSpec{Owners: []string{"x"}}); err == nil {
		t.Error("expected error when namespace is empty")
	}
	if _, _, err := RoleBindingManifest(RoleBindingSpec{Namespace: "ns"}); err == nil {
		t.Error("expected error when no owners are given")
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

func TestEgressFirewallManifest(t *testing.T) {
	path, content, err := EgressFirewallManifest(EgressFirewallSpec{
		Namespace: "tenant-a",
		Rules: []EgressRule{
			{Action: "Allow", CIDR: "10.0.0.0/8"},
			{Action: "Allow", DNSName: "registry.example.com", Ports: []EgressPort{{Protocol: "TCP", Port: 443}}},
			{Action: "Deny", CIDR: "0.0.0.0/0"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "tenant-a/egressfirewalls/default.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	for _, want := range []string{
		"kind: EgressFirewall",
		"name: default", // OVN-K requires the singleton be named default
		"namespace: tenant-a",
		"type: Allow",
		"cidrSelector: 10.0.0.0/8",
		"dnsName: registry.example.com",
		"protocol: TCP",
		"port: 443",
		"type: Deny",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("EgressFirewall missing %q:\n%s", want, y)
		}
	}
}

func TestEgressFirewallValidate(t *testing.T) {
	rule := []EgressRule{{Action: "Allow", CIDR: "1.0.0.0/8"}}
	if _, _, err := EgressFirewallManifest(EgressFirewallSpec{Rules: rule}); err == nil {
		t.Error("expected error without a namespace")
	}
	if _, _, err := EgressFirewallManifest(EgressFirewallSpec{Namespace: "n"}); err == nil {
		t.Error("expected error without rules")
	}
	// Exactly one destination per rule (XOR cidr/dnsName).
	if _, _, err := EgressFirewallManifest(EgressFirewallSpec{Namespace: "n", Rules: []EgressRule{{Action: "Allow"}}}); err == nil {
		t.Error("expected error when neither cidr nor dnsName is set")
	}
	if _, _, err := EgressFirewallManifest(EgressFirewallSpec{Namespace: "n", Rules: []EgressRule{{Action: "Allow", CIDR: "1.0.0.0/8", DNSName: "x"}}}); err == nil {
		t.Error("expected error when both cidr and dnsName are set")
	}
	if _, _, err := EgressFirewallManifest(EgressFirewallSpec{Namespace: "n", Rules: []EgressRule{{Action: "Permit", CIDR: "1.0.0.0/8"}}}); err == nil {
		t.Error("expected error for an action other than Allow/Deny")
	}
}

func TestEgressIPManifest(t *testing.T) {
	path, content, err := EgressIPManifest(EgressIPSpec{
		Name: "team-a-snat", EgressIPs: []string{"192.0.2.10", "192.0.2.11"}, Namespaces: []string{"team-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "egressips/team-a-snat.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	for _, want := range []string{
		"kind: EgressIP",
		"name: team-a-snat",
		"192.0.2.10",
		"192.0.2.11",
		"kubernetes.io/metadata.name",
		"team-a",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("EgressIP missing %q:\n%s", want, y)
		}
	}
	// Validation: name, IPs, and namespaces are all required.
	if _, _, err := EgressIPManifest(EgressIPSpec{EgressIPs: []string{"1.1.1.1"}, Namespaces: []string{"n"}}); err == nil {
		t.Error("expected error without a name")
	}
	if _, _, err := EgressIPManifest(EgressIPSpec{Name: "x", Namespaces: []string{"n"}}); err == nil {
		t.Error("expected error without egress IPs")
	}
	if _, _, err := EgressIPManifest(EgressIPSpec{Name: "x", EgressIPs: []string{"1.1.1.1"}}); err == nil {
		t.Error("expected error without namespaces")
	}
}

func TestExternalRouteManifest(t *testing.T) {
	path, content, err := ExternalRouteManifest(ExternalRouteSpec{
		Name: "team-a-gw", Namespaces: []string{"team-a"}, NextHops: []string{"10.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "externalroutes/team-a-gw.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	for _, want := range []string{
		"kind: AdminPolicyBasedExternalRoute",
		"name: team-a-gw",
		"namespaceSelector",
		"team-a",
		"static:",
		"ip: 10.0.0.1",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("AdminPolicyBasedExternalRoute missing %q:\n%s", want, y)
		}
	}
	if _, _, err := ExternalRouteManifest(ExternalRouteSpec{Namespaces: []string{"n"}, NextHops: []string{"1.1.1.1"}}); err == nil {
		t.Error("expected error without a name")
	}
	if _, _, err := ExternalRouteManifest(ExternalRouteSpec{Name: "x", NextHops: []string{"1.1.1.1"}}); err == nil {
		t.Error("expected error without namespaces")
	}
	if _, _, err := ExternalRouteManifest(ExternalRouteSpec{Name: "x", Namespaces: []string{"n"}}); err == nil {
		t.Error("expected error without next-hops")
	}
}

func TestNetworkPolicyManifest(t *testing.T) {
	path, content, err := NetworkPolicyManifest(NetworkPolicySpec{
		Name: "web-allow-db", Namespace: "team-a",
		AppliedTo: map[string]string{"app": "db"},
		Ingress: []PolicyRule{
			{From: []map[string]string{{"app": "web"}}, Ports: []PolicyPort{{Protocol: "TCP", Port: 5432}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "team-a/networkpolicies/web-allow-db.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	for _, want := range []string{
		"kind: NetworkPolicy",
		"name: web-allow-db",
		"namespace: team-a",
		"policyTypes",
		"Ingress",
		"app: db",  // applied-to Group
		"app: web", // peer Group
		"port: 5432",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("NetworkPolicy missing %q:\n%s", want, y)
		}
	}
}

func TestNetworkPolicyWholeNamespace(t *testing.T) {
	// No AppliedTo → an empty podSelector ({}) that selects every pod in the namespace.
	_, content, err := NetworkPolicyManifest(NetworkPolicySpec{Name: "default-deny", Namespace: "team-a"})
	if err != nil {
		t.Fatal(err)
	}
	y := string(content)
	if !strings.Contains(y, "podSelector: {}") {
		t.Errorf("expected an empty podSelector for a whole-namespace policy:\n%s", y)
	}
	// Name + namespace are required.
	if _, _, err := NetworkPolicyManifest(NetworkPolicySpec{Name: "x"}); err == nil {
		t.Error("expected error without a namespace")
	}
}

func TestAdminNetworkPolicyManifest(t *testing.T) {
	path, content, err := AdminNetworkPolicyManifest(AdminNetworkPolicySpec{
		Name: "tenant-isolation", Priority: 10,
		Subject: map[string]string{"tier": "prod"},
		Ingress: []AdminPolicyRule{
			{Action: "Pass", Peers: []map[string]string{{"tier": "prod"}}},
			{Action: "Deny", Peers: []map[string]string{{}}, Ports: []PolicyPort{{Protocol: "TCP", Port: 22}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "adminnetworkpolicies/tenant-isolation.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	for _, want := range []string{
		"kind: AdminNetworkPolicy",
		"name: tenant-isolation",
		"priority: 10",
		"action: Pass", // ANP-only action
		"action: Deny",
		"namespaces:",
		"tier: prod",
		"portNumber",
		"port: 22",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("ANP missing %q:\n%s", want, y)
		}
	}
}

func TestNameValidationRejectsPathTraversal(t *testing.T) {
	// Every name becomes both a metadata.name and a repo file-path segment, so a
	// traversal ("../x"), a separator ("a/b"), or a non-DNS-1123 name must be
	// rejected before it can escape its intended directory in the staged repo.
	for _, bad := range []string{"../evil", "a/b", "..", "UPPER", "with space", "under_score", ""} {
		if _, _, err := Manifest(Spec{Name: bad, Namespace: "tenant-a"}); err == nil {
			t.Errorf("projectUDN accepted invalid name %q", bad)
		}
		if _, _, err := Manifest(Spec{Name: "ok", Namespace: bad}); err == nil {
			t.Errorf("projectUDN accepted invalid namespace %q", bad)
		}
		if _, _, err := EgressIPManifest(EgressIPSpec{Name: bad, EgressIPs: []string{"1.1.1.1"}, Namespaces: []string{"n"}}); err == nil {
			t.Errorf("EgressIP accepted invalid name %q", bad)
		}
		if _, _, err := NetworkPolicyManifest(NetworkPolicySpec{Name: bad, Namespace: "n"}); err == nil {
			t.Errorf("NetworkPolicy accepted invalid name %q", bad)
		}
		if _, _, err := AdminNetworkPolicyManifest(AdminNetworkPolicySpec{Name: bad, Priority: 1}); err == nil {
			t.Errorf("ANP accepted invalid name %q", bad)
		}
		if _, _, err := NamespaceManifest(NamespaceSpec{Name: bad, Project: "p"}); err == nil {
			t.Errorf("NamespaceManifest accepted invalid namespace %q", bad)
		}
	}
}

func TestCIDRAndIPValidation(t *testing.T) {
	// Bad CIDRs/IPs render a manifest OVN-K rejects at apply — catch them early.
	if _, _, err := Manifest(Spec{Name: "n", Namespace: "ns", Subnets: []string{"10.0.0.0"}}); err == nil {
		t.Error("expected error for a subnet with no mask")
	}
	if _, _, err := NamespaceManifest(NamespaceSpec{Name: "n", Project: "p", VMNetwork: &PrimaryNet{Name: "vmnet", Subnet: "not-a-cidr"}}); err == nil {
		t.Error("expected error for a non-CIDR VM Network subnet")
	}
	if _, _, err := EgressFirewallManifest(EgressFirewallSpec{Namespace: "n", Rules: []EgressRule{{Action: "Allow", CIDR: "999.0.0.0/8"}}}); err == nil {
		t.Error("expected error for an invalid rule CIDR")
	}
	if _, _, err := EgressIPManifest(EgressIPSpec{Name: "s", EgressIPs: []string{"not-an-ip"}, Namespaces: []string{"n"}}); err == nil {
		t.Error("expected error for an invalid egress IP")
	}
	if _, _, err := ExternalRouteManifest(ExternalRouteSpec{Name: "r", Namespaces: []string{"n"}, NextHops: []string{"nope"}}); err == nil {
		t.Error("expected error for an invalid next-hop IP")
	}
}

func TestNamespaceManifestVMNetworkNoSyncWave(t *testing.T) {
	// The UDN and its Namespace share one platform Application; a negative sync-wave
	// would apply the UDN before its namespace exists and wedge the sync, so none is set.
	_, content, err := NamespaceManifest(NamespaceSpec{
		Name: "tenant-c", Project: "team-c", VMNetwork: &PrimaryNet{Name: "n", Subnet: "10.40.0.0/16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "sync-wave") {
		t.Errorf("VM Network UDN must not carry a sync-wave:\n%s", content)
	}
}

func TestBaselineAdminNetworkPolicy(t *testing.T) {
	// A baseline policy is the singleton named "default", carries no priority, and
	// rejects the Pass action.
	path, content, err := AdminNetworkPolicyManifest(AdminNetworkPolicySpec{
		Name: "ignored", Baseline: true, Priority: 99,
		Ingress: []AdminPolicyRule{{Action: "Deny", Peers: []map[string]string{{}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "baselineadminnetworkpolicies/default.yaml" {
		t.Errorf("path = %q", path)
	}
	y := string(content)
	if !strings.Contains(y, "kind: BaselineAdminNetworkPolicy") || !strings.Contains(y, "name: default") {
		t.Errorf("expected a BaselineAdminNetworkPolicy named default:\n%s", y)
	}
	if strings.Contains(y, "priority") {
		t.Errorf("a baseline policy must not carry a priority:\n%s", y)
	}
	// Pass is not a valid baseline action.
	if _, _, err := AdminNetworkPolicyManifest(AdminNetworkPolicySpec{
		Baseline: true, Ingress: []AdminPolicyRule{{Action: "Pass", Peers: []map[string]string{{}}}},
	}); err == nil {
		t.Error("expected error for a Pass action in a baseline policy")
	}
	// ANP requires a name and a valid priority.
	if _, _, err := AdminNetworkPolicyManifest(AdminNetworkPolicySpec{Priority: 5}); err == nil {
		t.Error("expected error for an ANP without a name")
	}
	if _, _, err := AdminNetworkPolicyManifest(AdminNetworkPolicySpec{Name: "x", Priority: 2000}); err == nil {
		t.Error("expected error for an out-of-range priority")
	}
}
