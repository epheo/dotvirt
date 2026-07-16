// Package netgen renders OVN-K user-defined networks and nmstate uplink policies
// — the manifests behind dotvirt's "Distributed Port Group" and "Uplink" creates
// — from small specs, the way vmgen renders VirtualMachines. Owns-nothing: the
// output is proposed via PR and applied by Argo, never written to the cluster.
package netgen

import (
	"fmt"
	"net"

	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/validate"
)

// validCIDR reports whether s parses as a CIDR (e.g. 10.0.0.0/24). Subnet/egress
// values only ever land in YAML scalars, so this is correctness, not safety: a bad
// value would otherwise render a manifest OVN-K rejects at apply time. The raw value
// is validated (no trimming) so what passes here is exactly what the manifest emits.
func validCIDR(s string) bool {
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

// validIP reports whether s parses as a bare IP address.
func validIP(s string) bool {
	return net.ParseIP(s) != nil
}

// requireCIDRs validates each subnet as a CIDR.
func requireCIDRs(cidrs []string) error {
	for _, c := range cidrs {
		if !validCIDR(c) {
			return fmt.Errorf("subnet %q must be a CIDR (e.g. 10.0.0.0/24)", c)
		}
	}
	return nil
}

// Scope of a port group create.
const (
	ScopeProject = "project" // namespace-scoped Layer2 UDN — an "Internal" port group
	ScopeShared  = "shared"  // cluster-scoped Layer2 CUDN — an isolated port group shared across namespaces
	ScopeVLAN    = "vlan"    // cluster-scoped localnet CUDN — a "VLAN" port group
)

// Spec describes a port group to create. Project scope emits a namespace-scoped
// Layer2 UserDefinedNetwork; VLAN scope emits a cluster-scoped localnet
// ClusterUserDefinedNetwork bound to an uplink (physical network) and a VLAN id.
type Spec struct {
	Name      string   `json:"name"`
	Scope     string   `json:"scope"`
	Namespace string   `json:"namespace,omitempty"` // project scope: the UDN's namespace
	Subnets   []string `json:"subnets,omitempty"`   // optional L2 CIDRs; empty = no IPAM

	// VLAN scope (localnet CUDN):
	VLAN            int      `json:"vlan,omitempty"`            // 802.1q access VLAN id
	PhysicalNetwork string   `json:"physicalNetwork,omitempty"` // the uplink's physical-network name
	Namespaces      []string `json:"namespaces,omitempty"`      // namespaces the CUDN publishes to
}

// Manifest renders the port group YAML plus its repo-relative path. Project
// networks land in the owning namespace's tree; VLAN (CUDN) networks are
// cluster-scoped and land under the (platform) repo's networks/ dir.
func Manifest(s Spec) (path string, content []byte, err error) {
	switch s.Scope {
	case "", ScopeProject:
		return projectUDN(s)
	case ScopeShared:
		return sharedCUDN(s)
	case ScopeVLAN:
		return vlanCUDN(s)
	default:
		return "", nil, fmt.Errorf("unsupported network scope %q", s.Scope)
	}
}

// projectUDN is an internal (Layer2, secondary) namespace-scoped port group.
func projectUDN(s Spec) (string, []byte, error) {
	if err := validate.RequireDNS1123("network name", s.Name); err != nil {
		return "", nil, err
	}
	if err := validate.RequireDNS1123("namespace", s.Namespace); err != nil {
		return "", nil, err
	}
	if err := requireCIDRs(s.Subnets); err != nil {
		return "", nil, err
	}
	layer2 := map[string]any{"role": "Secondary"}
	if len(s.Subnets) > 0 {
		layer2["subnets"] = toAny(s.Subnets)
	} else {
		// A subnet-less L2 network is a pure switch (no IPAM). OVN-K defaults
		// ipam.mode to Enabled (which then requires subnets), so we must set it
		// Disabled explicitly — valid here because this network is Secondary.
		layer2["ipam"] = map[string]any{"mode": "Disabled"}
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "k8s.ovn.org/v1",
		"kind":       "UserDefinedNetwork",
		"metadata":   map[string]any{"name": s.Name, "namespace": s.Namespace},
		"spec":       map[string]any{"topology": "Layer2", "layer2": layer2},
	})
	if err != nil {
		return "", nil, err
	}
	return s.Namespace + "/networks/" + s.Name + ".yaml", out, nil
}

// sharedCUDN is an isolated (Layer2, secondary) cluster-scoped port group spanning
// the selected namespaces — like vlanCUDN but a plain L2 segment with no uplink or
// VLAN. Cluster-scoped, so it lands under the (platform) repo's networks/ dir.
func sharedCUDN(s Spec) (string, []byte, error) {
	if err := validate.RequireDNS1123("network name", s.Name); err != nil {
		return "", nil, err
	}
	if len(s.Namespaces) == 0 {
		return "", nil, fmt.Errorf("at least one namespace must be selected")
	}
	if err := requireCIDRs(s.Subnets); err != nil {
		return "", nil, err
	}
	layer2 := map[string]any{"role": "Secondary"}
	if len(s.Subnets) > 0 {
		layer2["subnets"] = toAny(s.Subnets)
	} else {
		// Secondary networks may disable IPAM; OVN-K otherwise defaults it on.
		layer2["ipam"] = map[string]any{"mode": "Disabled"}
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "k8s.ovn.org/v1",
		"kind":       "ClusterUserDefinedNetwork",
		"metadata":   map[string]any{"name": s.Name},
		"spec": map[string]any{
			"namespaceSelector": map[string]any{
				"matchExpressions": []any{map[string]any{
					"key":      "kubernetes.io/metadata.name",
					"operator": "In",
					"values":   toAny(s.Namespaces),
				}},
			},
			"network": map[string]any{"topology": "Layer2", "layer2": layer2},
		},
	})
	if err != nil {
		return "", nil, err
	}
	return "networks/" + s.Name + ".yaml", out, nil
}

// vlanCUDN is a VLAN-backed (localnet, secondary) cluster-scoped port group: it
// rides an uplink (physicalNetworkName) on an access VLAN, published to the
// selected namespaces.
func vlanCUDN(s Spec) (string, []byte, error) {
	switch {
	case !validate.DNS1123Name(s.Name):
		return "", nil, fmt.Errorf("network name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", s.Name)
	case s.PhysicalNetwork == "":
		return "", nil, fmt.Errorf("an uplink (physical network) is required for a VLAN network")
	case s.VLAN <= 0 || s.VLAN > 4094:
		return "", nil, fmt.Errorf("a VLAN id in 1..4094 is required")
	case len(s.Namespaces) == 0:
		return "", nil, fmt.Errorf("at least one namespace must be selected")
	}
	if err := requireCIDRs(s.Subnets); err != nil {
		return "", nil, err
	}
	localnet := map[string]any{
		"role":                "Secondary",
		"physicalNetworkName": s.PhysicalNetwork,
		"ipam":                map[string]any{"mode": "Disabled"},
		"vlan": map[string]any{
			"mode":   "Access",
			"access": map[string]any{"id": s.VLAN},
		},
	}
	if len(s.Subnets) > 0 {
		localnet["subnets"] = toAny(s.Subnets)
		localnet["ipam"] = map[string]any{"mode": "Enabled"}
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "k8s.ovn.org/v1",
		"kind":       "ClusterUserDefinedNetwork",
		"metadata":   map[string]any{"name": s.Name},
		"spec": map[string]any{
			"namespaceSelector": map[string]any{
				"matchExpressions": []any{map[string]any{
					"key":      "kubernetes.io/metadata.name",
					"operator": "In",
					"values":   toAny(s.Namespaces),
				}},
			},
			"network": map[string]any{"topology": "Localnet", "localnet": localnet},
		},
	})
	if err != nil {
		return "", nil, err
	}
	return "networks/" + s.Name + ".yaml", out, nil
}

// NamespaceSpec describes a namespace to create within a project, optionally with
// a primary "VM Network" (a primary UDN). A primary UDN requires a fresh,
// labeled namespace, so the two are created together in one manifest.
type NamespaceSpec struct {
	Name      string      `json:"name"`
	Project   string      `json:"project"` // dotvirt.io/project label
	Repo      string      `json:"repo"`    // dotvirt.io/repo annotation
	VMNetwork *PrimaryNet `json:"vmNetwork,omitempty"`
}

// PrimaryNet is the namespace's default "VM Network" — a primary Layer2 UDN. It's
// always Layer2 on purpose: a flat L2 segment follows a VM across nodes on live
// migration (the VM keeps its IP), whereas Layer3's per-node subnets don't suit
// VMs — so topology isn't a user choice here.
type PrimaryNet struct {
	Name   string `json:"name"`
	Subnet string `json:"subnet"` // required CIDR — a primary UDN must do IPAM (see below)
}

// NamespaceManifest renders the Namespace (labeled into the project) plus, when a
// VM Network is requested, its primary UDN — as one multi-document file. A primary
// UDN needs the namespace label + an empty namespace, both of which hold because
// the namespace is created in the same change.
func NamespaceManifest(s NamespaceSpec) (path string, content []byte, err error) {
	if err := validate.RequireDNS1123("namespace", s.Name); err != nil {
		return "", nil, err
	}
	if s.Project == "" {
		return "", nil, fmt.Errorf("a project is required")
	}
	labels := map[string]any{"dotvirt.io/project": s.Project}
	if s.VMNetwork != nil {
		// Required for OVN-K to treat a UDN in this namespace as primary.
		labels["k8s.ovn.org/primary-user-defined-network"] = ""
	}
	meta := map[string]any{"name": s.Name, "labels": labels}
	if s.Repo != "" {
		meta["annotations"] = map[string]any{"dotvirt.io/repo": s.Repo}
	}
	ns, err := yaml.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Namespace", "metadata": meta,
	})
	if err != nil {
		return "", nil, err
	}
	docs := [][]byte{ns}

	if p := s.VMNetwork; p != nil {
		if err := validate.RequireDNS1123("VM Network name", p.Name); err != nil {
			return "", nil, err
		}
		// A primary UDN must do IPAM: OVN-K rejects a subnet-less primary network
		// and only allows ipam.mode=Disabled on Secondary networks, so unlike the
		// secondary projectUDN above, a VM Network's subnet is mandatory.
		if !validCIDR(p.Subnet) {
			return "", nil, fmt.Errorf("the VM Network needs a subnet CIDR (a primary network must do IPAM)")
		}
		layer2 := map[string]any{"role": "Primary", "subnets": []any{p.Subnet}}
		// The UDN and its Namespace commit to the same (platform) Application, so they
		// share a sync wave: ArgoCD's built-in kind ordering already applies the
		// Namespace before the UserDefinedNetwork. An explicit sync-wave here would
		// have to be >= the Namespace's (default 0); a negative wave inverts the order
		// and wedges the sync on "namespace not found", so we set none.
		udn, err := yaml.Marshal(map[string]any{
			"apiVersion": "k8s.ovn.org/v1",
			"kind":       "UserDefinedNetwork",
			"metadata":   map[string]any{"name": p.Name, "namespace": s.Name},
			"spec":       map[string]any{"topology": "Layer2", "layer2": layer2},
		})
		if err != nil {
			return "", nil, err
		}
		docs = append(docs, udn)
	}

	out := docs[0]
	for _, d := range docs[1:] {
		out = append(out, append([]byte("---\n"), d...)...)
	}
	return "namespaces/" + s.Name + ".yaml", out, nil
}

// RoleBindingSpec grants a tenant's owners admin on one of its namespaces — the
// RBAC delegation that turns a project from a folder into a real tenant. The
// RoleBinding is cluster-tenancy, so it is committed to the PLATFORM repo (granting
// access is an admin op) and applied by the platform Application, never a tenant.
type RoleBindingSpec struct {
	Namespace string   `json:"namespace"`
	Project   string   `json:"project"`        // dotvirt.io/project label
	Owners    []string `json:"owners"`         // usernames granted the role
	Role      string   `json:"role,omitempty"` // ClusterRole to bind; default "admin"
}

// RoleBindingManifest renders a namespace-scoped RoleBinding granting each owner the
// given ClusterRole (default "admin" — the built-in namespace-admin role) on the
// tenant namespace. One subject per owner (kind User).
func RoleBindingManifest(s RoleBindingSpec) (path string, content []byte, err error) {
	if err := validate.RequireDNS1123("namespace", s.Namespace); err != nil {
		return "", nil, err
	}
	if len(s.Owners) == 0 {
		return "", nil, fmt.Errorf("at least one owner is required")
	}
	role := s.Role
	if role == "" {
		role = "admin" // the built-in namespace-admin ClusterRole
	}
	subjects := make([]any, 0, len(s.Owners))
	for _, o := range s.Owners {
		if o == "" {
			return "", nil, fmt.Errorf("an owner name cannot be empty")
		}
		subjects = append(subjects, map[string]any{
			"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": o,
		})
	}
	meta := map[string]any{"name": s.Namespace + "-admins", "namespace": s.Namespace}
	if s.Project != "" {
		meta["labels"] = map[string]any{"dotvirt.io/project": s.Project}
	}
	rb, err := yaml.Marshal(map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "RoleBinding",
		"metadata":   meta,
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": role,
		},
		"subjects": subjects,
	})
	if err != nil {
		return "", nil, err
	}
	return "rbac/" + s.Namespace + ".yaml", rb, nil
}

// UplinkSpec describes a physical-network attachment to create: an OVS bridge
// enslaving a NIC, mapped to a localnet physical-network name, across a set of
// nodes — the vDS-uplink analog, as an nmstate NodeNetworkConfigurationPolicy.
type UplinkSpec struct {
	Name         string            `json:"name"`                   // physical-network name (the localnet mapping)
	Bridge       string            `json:"bridge,omitempty"`       // OVS bridge to create; default br-<name>
	NIC          string            `json:"nic"`                    // physical port to enslave
	NodeSelector map[string]string `json:"nodeSelector,omitempty"` // node subset; empty = all worker nodes
}

// UplinkManifest renders the NNCP YAML plus its repo-relative path.
func UplinkManifest(s UplinkSpec) (path string, content []byte, err error) {
	if err := validate.RequireDNS1123("uplink name", s.Name); err != nil {
		return "", nil, err
	}
	if s.NIC == "" {
		return "", nil, fmt.Errorf("a NIC is required")
	}
	bridge := s.Bridge
	if bridge == "" {
		bridge = "br-" + s.Name
	}
	sel := s.NodeSelector
	if len(sel) == 0 {
		sel = map[string]string{"node-role.kubernetes.io/worker": ""}
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "nmstate.io/v1",
		"kind":       "NodeNetworkConfigurationPolicy",
		"metadata":   map[string]any{"name": "uplink-" + s.Name},
		"spec": map[string]any{
			"nodeSelector": toStrAny(sel),
			"desiredState": map[string]any{
				"interfaces": []any{map[string]any{
					"name":  bridge,
					"type":  "ovs-bridge",
					"state": "up",
					"bridge": map[string]any{
						"options": map[string]any{"stp": false},
						"port":    []any{map[string]any{"name": s.NIC}},
					},
				}},
				"ovn": map[string]any{
					"bridge-mappings": []any{map[string]any{
						"localnet": s.Name, "bridge": bridge, "state": "present",
					}},
				},
			},
		},
	})
	if err != nil {
		return "", nil, err
	}
	return "uplinks/" + s.Name + ".yaml", out, nil
}

// EgressFirewallSpec describes a namespace's north-south egress firewall — the
// gateway-firewall analog on a project's Tier-1. OVN-K allows exactly one
// EgressFirewall per namespace and it must be named "default"; its ordered rules
// permit or deny egress from the namespace's pods (and VMs) to external CIDRs or DNS
// names, optionally narrowed to a port. Namespace-scoped, so it rides the tenant's
// own repo (unlike a cluster-scoped CUDN/uplink).
type EgressFirewallSpec struct {
	Namespace string       `json:"namespace"`
	Rules     []EgressRule `json:"rules"`
}

// EgressRule is one ordered allow/deny against an external destination. Exactly one
// of CIDR or DNSName names the destination; Ports optionally narrows it.
type EgressRule struct {
	Action  string       `json:"action"`            // Allow | Deny
	CIDR    string       `json:"cidr,omitempty"`    // cidrSelector destination
	DNSName string       `json:"dnsName,omitempty"` // dnsName destination
	Ports   []EgressPort `json:"ports,omitempty"`
}

// EgressPort narrows a rule to a transport port.
type EgressPort struct {
	Protocol string `json:"protocol"` // TCP | UDP | SCTP
	Port     int    `json:"port"`
}

// EgressFirewallManifest renders the EgressFirewall YAML plus its repo-relative
// path. The object is always named "default" (OVN-K permits one per namespace); the
// rules render in order, since an EgressFirewall is first-match.
func EgressFirewallManifest(s EgressFirewallSpec) (path string, content []byte, err error) {
	if err := validate.RequireDNS1123("namespace", s.Namespace); err != nil {
		return "", nil, err
	}
	if len(s.Rules) == 0 {
		return "", nil, fmt.Errorf("at least one egress rule is required")
	}
	egress := make([]any, 0, len(s.Rules))
	for i, r := range s.Rules {
		if r.Action != "Allow" && r.Action != "Deny" {
			return "", nil, fmt.Errorf("rule %d: action must be Allow or Deny", i+1)
		}
		// Exactly one destination: a CIDR or a DNS name (XOR).
		if (r.CIDR == "") == (r.DNSName == "") {
			return "", nil, fmt.Errorf("rule %d: set exactly one of cidr or dnsName", i+1)
		}
		if r.CIDR != "" && !validCIDR(r.CIDR) {
			return "", nil, fmt.Errorf("rule %d: cidr %q must be a CIDR (e.g. 0.0.0.0/0)", i+1, r.CIDR)
		}
		to := map[string]any{}
		if r.CIDR != "" {
			to["cidrSelector"] = r.CIDR
		} else {
			to["dnsName"] = r.DNSName
		}
		rule := map[string]any{"type": r.Action, "to": to}
		if len(r.Ports) > 0 {
			ports := make([]any, 0, len(r.Ports))
			for j, p := range r.Ports {
				if p.Protocol != "TCP" && p.Protocol != "UDP" && p.Protocol != "SCTP" {
					return "", nil, fmt.Errorf("rule %d port %d: protocol must be TCP, UDP or SCTP", i+1, j+1)
				}
				if p.Port <= 0 || p.Port > 65535 {
					return "", nil, fmt.Errorf("rule %d port %d: port must be 1..65535", i+1, j+1)
				}
				ports = append(ports, map[string]any{"protocol": p.Protocol, "port": p.Port})
			}
			rule["ports"] = ports
		}
		egress = append(egress, rule)
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "k8s.ovn.org/v1",
		"kind":       "EgressFirewall",
		"metadata":   map[string]any{"name": "default", "namespace": s.Namespace},
		"spec":       map[string]any{"egress": egress},
	})
	if err != nil {
		return "", nil, err
	}
	return s.Namespace + "/egressfirewalls/default.yaml", out, nil
}

// EgressIPSpec describes a cluster-scoped EgressIP — the Tier-0 source-NAT pool that
// pins a project's egress to fixed, routable IPs. OVN-K applies it to the namespaces
// its selector matches; we render a namespaceSelector matching the chosen namespaces
// by name. Cluster-scoped, so it lands in the platform repo.
type EgressIPSpec struct {
	Name       string   `json:"name"`
	EgressIPs  []string `json:"egressIPs"`
	Namespaces []string `json:"namespaces"`
}

// EgressIPManifest renders the EgressIP YAML plus its repo-relative path.
func EgressIPManifest(s EgressIPSpec) (path string, content []byte, err error) {
	switch {
	case !validate.DNS1123Name(s.Name):
		return "", nil, fmt.Errorf("name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", s.Name)
	case len(s.EgressIPs) == 0:
		return "", nil, fmt.Errorf("at least one egress IP is required")
	case len(s.Namespaces) == 0:
		return "", nil, fmt.Errorf("at least one namespace must be selected")
	}
	for _, ip := range s.EgressIPs {
		if !validIP(ip) {
			return "", nil, fmt.Errorf("egress IP %q must be an IP address", ip)
		}
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "k8s.ovn.org/v1",
		"kind":       "EgressIP",
		"metadata":   map[string]any{"name": s.Name},
		"spec": map[string]any{
			"egressIPs": toAny(s.EgressIPs),
			"namespaceSelector": map[string]any{
				"matchExpressions": []any{map[string]any{
					"key":      "kubernetes.io/metadata.name",
					"operator": "In",
					"values":   toAny(s.Namespaces),
				}},
			},
		},
	})
	if err != nil {
		return "", nil, err
	}
	return "egressips/" + s.Name + ".yaml", out, nil
}

// ExternalRouteSpec describes a cluster-scoped AdminPolicyBasedExternalRoute — the
// Tier-0 static route that steers a project's egress through external next-hop
// gateways. Cluster-scoped, so it lands in the platform repo.
type ExternalRouteSpec struct {
	Name       string   `json:"name"`
	Namespaces []string `json:"namespaces"`
	NextHops   []string `json:"nextHops"` // static next-hop IPs
}

// ExternalRouteManifest renders the AdminPolicyBasedExternalRoute YAML plus its
// repo-relative path.
func ExternalRouteManifest(s ExternalRouteSpec) (path string, content []byte, err error) {
	switch {
	case !validate.DNS1123Name(s.Name):
		return "", nil, fmt.Errorf("name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", s.Name)
	case len(s.Namespaces) == 0:
		return "", nil, fmt.Errorf("at least one namespace must be selected")
	case len(s.NextHops) == 0:
		return "", nil, fmt.Errorf("at least one next-hop IP is required")
	}
	static := make([]any, 0, len(s.NextHops))
	for _, ip := range s.NextHops {
		if !validIP(ip) {
			return "", nil, fmt.Errorf("next-hop %q must be an IP address", ip)
		}
		static = append(static, map[string]any{"ip": ip})
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "k8s.ovn.org/v1",
		"kind":       "AdminPolicyBasedExternalRoute",
		"metadata":   map[string]any{"name": s.Name},
		"spec": map[string]any{
			"from": map[string]any{
				"namespaceSelector": map[string]any{
					"matchExpressions": []any{map[string]any{
						"key":      "kubernetes.io/metadata.name",
						"operator": "In",
						"values":   toAny(s.Namespaces),
					}},
				},
			},
			"nextHops": map[string]any{"static": static},
		},
	})
	if err != nil {
		return "", nil, err
	}
	return "externalroutes/" + s.Name + ".yaml", out, nil
}

// NetworkPolicySpec describes a NetworkPolicy — the east-west Distributed Firewall.
// It protects a Group (AppliedTo: a podSelector) inside one namespace, allowing
// ingress only from the peer Groups named in its rules (a NetworkPolicy that selects
// pods default-denies all other ingress). Namespace-scoped, so it rides the tenant's
// own repo. Groups are label selectors — the same primitive NSX-T's dynamic Groups
// compile to.
type NetworkPolicySpec struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	AppliedTo map[string]string `json:"appliedTo,omitempty"` // podSelector matchLabels; empty = the whole namespace
	Ingress   []PolicyRule      `json:"ingress,omitempty"`
}

// PolicyRule is one ingress rule: allow traffic from the peer Groups (From) to the
// applied-to Group, optionally narrowed to Ports. An empty From means "any source";
// an empty Ports means "any port".
type PolicyRule struct {
	From  []map[string]string `json:"from,omitempty"` // peer Groups (podSelector matchLabels)
	Ports []PolicyPort        `json:"ports,omitempty"`
}

// PolicyPort narrows a rule to a transport port.
type PolicyPort struct {
	Protocol string `json:"protocol"` // TCP | UDP | SCTP
	Port     int    `json:"port"`
}

// NetworkPolicyManifest renders the NetworkPolicy YAML plus its repo-relative path.
func NetworkPolicyManifest(s NetworkPolicySpec) (path string, content []byte, err error) {
	if err := validate.RequireDNS1123("name", s.Name); err != nil {
		return "", nil, err
	}
	if err := validate.RequireDNS1123("namespace", s.Namespace); err != nil {
		return "", nil, err
	}
	// An empty podSelector ({}) selects every pod in the namespace — the "applied to
	// the whole project" case; otherwise scope to the Group's labels.
	podSelector := map[string]any{}
	if len(s.AppliedTo) > 0 {
		podSelector = map[string]any{"matchLabels": toStrAny(s.AppliedTo)}
	}
	spec := map[string]any{"podSelector": podSelector, "policyTypes": []any{"Ingress"}}
	if len(s.Ingress) > 0 {
		ingress := make([]any, 0, len(s.Ingress))
		for _, r := range s.Ingress {
			rule := map[string]any{}
			if len(r.From) > 0 {
				from := make([]any, 0, len(r.From))
				for _, peer := range r.From {
					from = append(from, map[string]any{"podSelector": map[string]any{"matchLabels": toStrAny(peer)}})
				}
				rule["from"] = from
			}
			if len(r.Ports) > 0 {
				ports := make([]any, 0, len(r.Ports))
				for i, p := range r.Ports {
					if p.Protocol != "TCP" && p.Protocol != "UDP" && p.Protocol != "SCTP" {
						return "", nil, fmt.Errorf("rule port %d: protocol must be TCP, UDP or SCTP", i+1)
					}
					if p.Port <= 0 || p.Port > 65535 {
						return "", nil, fmt.Errorf("rule port %d: port must be 1..65535", i+1)
					}
					ports = append(ports, map[string]any{"protocol": p.Protocol, "port": p.Port})
				}
				rule["ports"] = ports
			}
			ingress = append(ingress, rule)
		}
		spec["ingress"] = ingress
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "NetworkPolicy",
		"metadata":   map[string]any{"name": s.Name, "namespace": s.Namespace},
		"spec":       spec,
	})
	if err != nil {
		return "", nil, err
	}
	return s.Namespace + "/networkpolicies/" + s.Name + ".yaml", out, nil
}

// AdminNetworkPolicySpec describes the cluster-wide admin DFW tier —
// AdminNetworkPolicy (priority-ordered, actions Allow/Deny/Pass) or, when Baseline,
// BaselineAdminNetworkPolicy (the cluster default: a singleton named "default", no
// priority, actions Allow/Deny only). Both override or backstop tenant
// NetworkPolicies, so they are cluster-scoped, platform-tier, and admin-only. Subject
// and peers are namespace selectors — Groups of projects.
type AdminNetworkPolicySpec struct {
	Name     string            `json:"name"`
	Baseline bool              `json:"baseline,omitempty"` // render a BaselineAdminNetworkPolicy
	Priority int               `json:"priority,omitempty"` // 0..1000, lower = higher precedence (ANP only)
	Subject  map[string]string `json:"subject,omitempty"`  // namespaceSelector matchLabels; empty = all namespaces
	Ingress  []AdminPolicyRule `json:"ingress,omitempty"`
	Egress   []AdminPolicyRule `json:"egress,omitempty"`
}

// AdminPolicyRule is one ordered admin rule: an action against the peer Groups
// (namespace selectors), optionally narrowed to ports.
type AdminPolicyRule struct {
	Action string              `json:"action"`          // Allow | Deny | Pass (Pass is ANP-only)
	Peers  []map[string]string `json:"peers,omitempty"` // namespaceSelector matchLabels — the from/to Groups
	Ports  []PolicyPort        `json:"ports,omitempty"`
}

// AdminNetworkPolicyManifest renders the (Baseline)AdminNetworkPolicy YAML plus its
// repo-relative path. A baseline policy is the cluster singleton "default" — no
// priority, and Pass is not a valid action.
func AdminNetworkPolicyManifest(s AdminNetworkPolicySpec) (path string, content []byte, err error) {
	name := s.Name
	if s.Baseline {
		name = "default" // BANP is a cluster singleton named default
	} else if !validate.DNS1123Name(name) {
		return "", nil, fmt.Errorf("name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", name)
	} else if s.Priority < 0 || s.Priority > 1000 {
		return "", nil, fmt.Errorf("priority must be 0..1000")
	}
	subjectSel := map[string]any{}
	if len(s.Subject) > 0 {
		subjectSel = map[string]any{"matchLabels": toStrAny(s.Subject)}
	}
	spec := map[string]any{"subject": map[string]any{"namespaces": subjectSel}}
	if !s.Baseline {
		spec["priority"] = s.Priority
	}
	renderRules := func(rules []AdminPolicyRule, peerKey string) ([]any, error) {
		out := make([]any, 0, len(rules))
		for i, r := range rules {
			switch r.Action {
			case "Allow", "Deny":
				// always valid
			case "Pass":
				if s.Baseline {
					return nil, fmt.Errorf("rule %d: a baseline policy has no Pass action (Allow or Deny only)", i+1)
				}
			default:
				return nil, fmt.Errorf("rule %d: action must be Allow, Deny or Pass", i+1)
			}
			if len(r.Peers) == 0 {
				return nil, fmt.Errorf("rule %d: at least one peer Group is required", i+1)
			}
			peers := make([]any, 0, len(r.Peers))
			for _, p := range r.Peers {
				sel := map[string]any{}
				if len(p) > 0 {
					sel = map[string]any{"matchLabels": toStrAny(p)}
				}
				peers = append(peers, map[string]any{"namespaces": sel})
			}
			rule := map[string]any{"action": r.Action, peerKey: peers}
			if len(r.Ports) > 0 {
				ports := make([]any, 0, len(r.Ports))
				for j, pt := range r.Ports {
					if pt.Protocol != "TCP" && pt.Protocol != "UDP" && pt.Protocol != "SCTP" {
						return nil, fmt.Errorf("rule %d port %d: protocol must be TCP, UDP or SCTP", i+1, j+1)
					}
					if pt.Port <= 0 || pt.Port > 65535 {
						return nil, fmt.Errorf("rule %d port %d: port must be 1..65535", i+1, j+1)
					}
					ports = append(ports, map[string]any{"portNumber": map[string]any{"protocol": pt.Protocol, "port": pt.Port}})
				}
				rule["ports"] = ports
			}
			out = append(out, rule)
		}
		return out, nil
	}
	if len(s.Ingress) > 0 {
		ing, err := renderRules(s.Ingress, "from")
		if err != nil {
			return "", nil, err
		}
		spec["ingress"] = ing
	}
	if len(s.Egress) > 0 {
		eg, err := renderRules(s.Egress, "to")
		if err != nil {
			return "", nil, err
		}
		spec["egress"] = eg
	}
	kind, dir := "AdminNetworkPolicy", "adminnetworkpolicies"
	if s.Baseline {
		kind, dir = "BaselineAdminNetworkPolicy", "baselineadminnetworkpolicies"
	}
	out, err := yaml.Marshal(map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	})
	if err != nil {
		return "", nil, err
	}
	return dir + "/" + name + ".yaml", out, nil
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func toStrAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
