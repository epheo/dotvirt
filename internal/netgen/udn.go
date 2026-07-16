package netgen

import (
	"fmt"

	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/validate"
)

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
