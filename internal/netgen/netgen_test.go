package netgen

import (
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/pkg/tenancy"
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

// A publish namespace never joins a project, by name or by label, and its
// own manifest labels it for the project and never into it.
func TestPublishNamespaces(t *testing.T) {
	if _, _, err := NamespaceManifest(NamespaceSpec{Name: "shop-publish", Project: "other"}); err == nil {
		t.Error("a namespace named like a publish namespace joined a project")
	}
	live := &LiveNamespace{Labels: map[string]string{tenancy.PublishLabel: "shop"}}
	if _, _, err := NamespaceManifest(NamespaceSpec{Name: "edge", Project: "other", Live: live}); err == nil {
		t.Error("a namespace labeled as a publish namespace joined a project")
	}
	path, content, err := PublishNamespaceManifest("shop")
	if err != nil {
		t.Fatal(err)
	}
	y := string(content)
	if path != "namespaces/shop-publish.yaml" || !strings.Contains(y, "name: shop-publish") || !strings.Contains(y, tenancy.PublishLabel+": shop") || strings.Contains(y, "dotvirt.io/project") {
		t.Errorf("%s:\n%s", path, y)
	}
	if _, _, err := PublishNamespaceManifest(strings.Repeat("a", 60)); err == nil {
		t.Error("a publish namespace name past 63 characters rendered")
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

// A project's sync account joins the owners in both grants, so the project's
// repo and its people hold the same rights, and without owners the grants
// still render: the project's repo syncs as that account.
func TestRoleBindingManifestSyncAccount(t *testing.T) {
	sa := ServiceAccountSubject("openshift-gitops", "dotvirt-p-team-a")
	for name, owners := range map[string][]string{"owners": {"alice"}, "no owners": nil} {
		spec := RoleBindingSpec{Namespace: "tenant-a", Project: "team-a", Owners: owners, SyncAccount: sa}
		path, admin, err := RoleBindingManifest(spec)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		role, subjects, ok, err := DeclaredGrant(admin, "tenant-a-admins")
		if err != nil || !ok || role != "admin" || path != "rbac/tenant-a.yaml" {
			t.Fatalf("%s: DeclaredGrant = %q %v %v at %s", name, role, ok, err, path)
		}
		if !slices.Contains(subjects, *sa) || len(subjects) != len(owners)+1 {
			t.Errorf("%s: admin subjects %+v, want the owners and the sync account", name, subjects)
		}
		path, egress, err := EgressManifest(spec)
		if err != nil {
			t.Fatal(err)
		}
		role, egressSubjects, ok, err := DeclaredGrant(egress, "tenant-a-egress")
		if err != nil || !ok || role != "dotvirt-tenant-egress" || path != "rbac/tenant-a/tenant-a-egress.yaml" {
			t.Fatalf("%s: egress grant = %q %v %v at %s", name, role, ok, err, path)
		}
		if !slices.Equal(egressSubjects, subjects) {
			t.Errorf("%s: egress subjects %+v, want the admin grant's %+v", name, egressSubjects, subjects)
		}
	}
	// Another project's sync account never survives a re-render: a namespace
	// grants one project's repo.
	other := *ServiceAccountSubject("openshift-gitops", "dotvirt-p-team-b")
	_, joined, err := RoleBindingManifest(RoleBindingSpec{Namespace: "ns", Kept: []Subject{other, {Kind: "User", APIGroup: rbacGroup, Name: "bob"}}, SyncAccount: sa})
	if err != nil {
		t.Fatal(err)
	}
	if _, subjects, _, _ := DeclaredGrant(joined, "ns-admins"); slices.Contains(subjects, other) || len(subjects) != 2 {
		t.Errorf("joined grant subjects %+v, want bob and this project's account", subjects)
	}
	// Kept subjects survive a re-render, each subject once.
	kept := []Subject{{Kind: "Group", APIGroup: rbacGroup, Name: "ops"}, *sa}
	_, content, err := RoleBindingManifest(RoleBindingSpec{Namespace: "ns", Kept: kept, SyncAccount: sa, Role: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	role, subjects, _, _ := DeclaredGrant(content, "ns-admins")
	if role != "edit" || !slices.Equal(subjects, kept) {
		t.Errorf("re-render = %q %+v, want edit %+v", role, subjects, kept)
	}
	// A grant naming no one is refused.
	if _, _, err := RoleBindingManifest(RoleBindingSpec{Namespace: "ns"}); err == nil {
		t.Error("a grant with no subject rendered")
	}
	if _, _, err := EgressManifest(RoleBindingSpec{Namespace: "ns"}); err == nil {
		t.Error("an egress grant with no subject rendered")
	}
}

func TestDeclaredGrant(t *testing.T) {
	file := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ns-admins\n---\n" +
		"apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: ns-admins\n  namespace: ns\n" +
		"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: admin\n" +
		"subjects:\n- kind: User\n  apiGroup: rbac.authorization.k8s.io\n  name: alice\n")
	role, subjects, ok, err := DeclaredGrant(file, "ns-admins")
	if err != nil || !ok || role != "admin" || len(subjects) != 1 || subjects[0].Name != "alice" {
		t.Fatalf("DeclaredGrant = %q %+v %v %v", role, subjects, ok, err)
	}
	if _, _, ok, err := DeclaredGrant(file, "other-admins"); ok || err != nil {
		t.Errorf("another grant: ok=%v err=%v", ok, err)
	}
	roleKind := []byte(strings.Replace(string(file), "kind: ClusterRole", "kind: Role", 1))
	if _, _, _, err := DeclaredGrant(roleKind, "ns-admins"); err == nil {
		t.Error("a binding of a namespaced Role read as a ClusterRole grant")
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
		"allow-extra-patch-ports: true", // OVN's localnet patch port must not fail verification
		"name: br-physnet-prod",         // default bridge
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
	// the selected namespaces - no uplink, no VLAN.
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
		"mode: Disabled", // no subnet -> IPAM disabled
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
			{Action: "Allow", DNSName: "registry.example.com", Ports: []PolicyPort{{Protocol: "TCP", Port: 443}}},
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
	// No AppliedTo -> an empty podSelector ({}) that selects every pod in the namespace.
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
	// Bad CIDRs/IPs render a manifest OVN-K rejects at apply - catch them early.
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
	// The API admits 100..1000, but OVN-K never programs them.
	if _, _, err := AdminNetworkPolicyManifest(AdminNetworkPolicySpec{Name: "x", Priority: 100}); err == nil || !strings.Contains(err.Error(), "0..99") {
		t.Errorf("priority 100 must be refused naming the range, got %v", err)
	}
	if _, _, err := AdminNetworkPolicyManifest(AdminNetworkPolicySpec{Name: "x", Priority: 99}); err != nil {
		t.Errorf("priority 99 is enforced and must be accepted: %v", err)
	}
}

// An adopted namespace keeps what it already carries; only the tenancy keys are
// dotvirt's to set.
func TestNamespaceManifestKeepsLiveMetadata(t *testing.T) {
	_, content, err := NamespaceManifest(NamespaceSpec{
		Name: "tenant-a", Project: "tenant-a", Repo: "acme/tenant-a.git",
		Live: &LiveNamespace{
			Labels:      map[string]string{"tenant": "a", primaryNetworkLabel: "", projectLabel: "stale"},
			Annotations: map[string]string{"openshift.io/description": "roundtable", repoAnnotation: "acme/old.git"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	y := string(content)
	for _, want := range []string{
		"tenant: a", `k8s.ovn.org/primary-user-defined-network: ""`, "dotvirt.io/project: tenant-a",
		"openshift.io/description: roundtable", "dotvirt.io/repo: acme/tenant-a.git",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("missing %q:\n%s", want, y)
		}
	}
	for _, stale := range []string{"stale", "acme/old.git"} {
		if strings.Contains(y, stale) {
			t.Errorf("live tenancy must yield to the spec, found %q:\n%s", stale, y)
		}
	}
}

// A release strips only dotvirt's tenancy; the namespace's own metadata stays,
// and a map emptied by the strip goes with it.
func TestReleasedNamespaceManifestKeepsForeignMetadata(t *testing.T) {
	out, err := ReleasedNamespaceManifest([]byte(`apiVersion: v1
kind: Namespace
metadata:
  annotations:
    dotvirt.io/repo: acme/tenant-a.git
    dotvirt.io/path: clusters/a
    openshift.io/description: roundtable
  labels:
    dotvirt.io/project: tenant-a
    k8s.ovn.org/primary-user-defined-network: ""
  name: tenant-a
`))
	if err != nil {
		t.Fatal(err)
	}
	y := string(out)
	for _, gone := range []string{"dotvirt.io/project", "dotvirt.io/repo", "dotvirt.io/path"} {
		if strings.Contains(y, gone) {
			t.Errorf("release must drop %s:\n%s", gone, y)
		}
	}
	for _, want := range []string{`k8s.ovn.org/primary-user-defined-network: ""`, "openshift.io/description: roundtable", "name: tenant-a"} {
		if !strings.Contains(y, want) {
			t.Errorf("release must keep %q:\n%s", want, y)
		}
	}
	out, err = ReleasedNamespaceManifest([]byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  labels:\n    dotvirt.io/project: p\n  name: n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "labels") {
		t.Errorf("an emptied labels map must go:\n%s", out)
	}
}

// Selector labels, namespace lists and DNS names all reach the API server
// verbatim: a value it rejects must fail here, not after merge at Argo apply.
func TestSelectorAndNamespaceValidation(t *testing.T) {
	badLabels := []map[string]string{{"bad key": "x"}, {"app": "has space"}, {"app": "a\nb"}}
	for _, l := range badLabels {
		if _, _, err := NetworkPolicyManifest(NetworkPolicySpec{Name: "p", Namespace: "n", AppliedTo: l}); err == nil {
			t.Errorf("NetworkPolicy accepted appliedTo %q", l)
		}
		if _, _, err := NetworkPolicyManifest(NetworkPolicySpec{Name: "p", Namespace: "n",
			Ingress: []PolicyRule{{From: []map[string]string{l}}}}); err == nil {
			t.Errorf("NetworkPolicy accepted peer %q", l)
		}
		if _, _, err := AdminNetworkPolicyManifest(AdminNetworkPolicySpec{Name: "a", Priority: 1, Subject: l}); err == nil {
			t.Errorf("ANP accepted subject %q", l)
		}
		if _, _, err := AdminNetworkPolicyManifest(AdminNetworkPolicySpec{Name: "a", Priority: 1,
			Egress: []AdminPolicyRule{{Action: "Deny", Peers: []map[string]string{l}}}}); err == nil {
			t.Errorf("ANP accepted peer %q", l)
		}
		if _, _, err := UplinkManifest(UplinkSpec{Name: "u", NIC: "eth1", NodeSelector: l}); err == nil {
			t.Errorf("uplink accepted node selector %q", l)
		}
	}

	for _, ns := range [][]string{{"ok", "Bad"}, {"a/b"}, {""}} {
		if _, _, err := Manifest(Spec{Name: "s", Scope: ScopeShared, Namespaces: ns}); err == nil {
			t.Errorf("shared network accepted namespaces %q", ns)
		}
		if _, _, err := Manifest(Spec{Name: "v", Scope: ScopeVLAN, VLAN: 10, PhysicalNetwork: "p", Namespaces: ns}); err == nil {
			t.Errorf("VLAN network accepted namespaces %q", ns)
		}
		if _, _, err := EgressIPManifest(EgressIPSpec{Name: "e", EgressIPs: []string{"1.1.1.1"}, Namespaces: ns}); err == nil {
			t.Errorf("EgressIP accepted namespaces %q", ns)
		}
		if _, _, err := ExternalRouteManifest(ExternalRouteSpec{Name: "r", NextHops: []string{"1.1.1.1"}, Namespaces: ns}); err == nil {
			t.Errorf("external route accepted namespaces %q", ns)
		}
	}

	for _, dns := range []string{"example.com", "*.example.com", "Example.COM.", "host"} {
		if _, _, err := EgressFirewallManifest(EgressFirewallSpec{Namespace: "n", Rules: []EgressRule{{Action: "Allow", DNSName: dns}}}); err != nil {
			t.Errorf("egress firewall refused dnsName %q: %v", dns, err)
		}
	}
	for _, dns := range []string{"a b.com", "a.*.com", "**.com", "x.com\n- to: {}", "http://x.com"} {
		if _, _, err := EgressFirewallManifest(EgressFirewallSpec{Namespace: "n", Rules: []EgressRule{{Action: "Allow", DNSName: dns}}}); err == nil {
			t.Errorf("egress firewall accepted dnsName %q", dns)
		}
	}
}

// The physical network is checked by the CUDN CRD's own rule (1..253 chars, no
// ',' or ':'), so a hand-made bridge mapping like physnet_1 stays usable. The
// NIC is a Linux interface name (at most 15 bytes, no '/', ':' or space); the
// OVS bridge is not, since nmstate accepts a longer name for a bridge with
// ports, so an uplink name past 12 characters stays usable.
func TestUplinkAndPhysicalNetworkNames(t *testing.T) {
	vlan := func(p string) error {
		_, _, err := Manifest(Spec{Name: "v", Scope: ScopeVLAN, VLAN: 10, PhysicalNetwork: p, Namespaces: []string{"n"}})
		return err
	}
	for _, ok := range []string{"physnet", "physnet_1", "Phys.Net-2", strings.Repeat("p", 253)} {
		if err := vlan(ok); err != nil {
			t.Errorf("physical network %q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"a,b", "a:b", strings.Repeat("p", 254)} {
		if err := vlan(bad); err == nil {
			t.Errorf("physical network %q accepted", bad)
		}
	}

	for _, ok := range []UplinkSpec{
		{Name: "physnet-prod", NIC: "eno2"},
		{Name: "u", NIC: "enp1s0f0.100"},
		{Name: "a-very-long-uplink", NIC: "bond0", Bridge: "br-long"},
		{Name: "a-very-long-uplink", NIC: "eth1"},
	} {
		if _, _, err := UplinkManifest(ok); err != nil {
			t.Errorf("uplink %+v refused: %v", ok, err)
		}
	}
	for _, bad := range []UplinkSpec{
		{Name: "u", NIC: "eth 1"},
		{Name: "u", NIC: "eth0:1"},
		{Name: "u", NIC: "a/b"},
		{Name: "u", NIC: "averyveryverylongnic"},
		{Name: "u", NIC: ".."},
	} {
		if _, _, err := UplinkManifest(bad); err == nil {
			t.Errorf("uplink %+v accepted", bad)
		}
	}
}
