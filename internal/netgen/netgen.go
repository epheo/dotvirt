// Package netgen renders OVN-K user-defined networks and nmstate uplink policies
// — the manifests behind dotvirt's "Distributed Port Group" and "Uplink" creates
// — from small specs, the way vmgen renders VirtualMachines. Owns-nothing: the
// output is proposed via PR and applied by Argo, never written to the cluster.
package netgen

import (
	"fmt"

	"sigs.k8s.io/yaml"
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
	if s.Name == "" || s.Namespace == "" {
		return "", nil, fmt.Errorf("name and namespace are required")
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
	switch {
	case s.Name == "":
		return "", nil, fmt.Errorf("name is required")
	case len(s.Namespaces) == 0:
		return "", nil, fmt.Errorf("at least one namespace must be selected")
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
	case s.Name == "":
		return "", nil, fmt.Errorf("name is required")
	case s.PhysicalNetwork == "":
		return "", nil, fmt.Errorf("an uplink (physical network) is required for a VLAN network")
	case s.VLAN <= 0 || s.VLAN > 4094:
		return "", nil, fmt.Errorf("a VLAN id in 1..4094 is required")
	case len(s.Namespaces) == 0:
		return "", nil, fmt.Errorf("at least one namespace must be selected")
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
	if s.Name == "" || s.Project == "" {
		return "", nil, fmt.Errorf("a namespace name and project are required")
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
		if p.Name == "" {
			return "", nil, fmt.Errorf("the VM Network needs a name")
		}
		// A primary UDN must do IPAM: OVN-K rejects a subnet-less primary network
		// and only allows ipam.mode=Disabled on Secondary networks, so unlike the
		// secondary projectUDN above, a VM Network's subnet is mandatory.
		if p.Subnet == "" {
			return "", nil, fmt.Errorf("the VM Network needs a subnet (a primary network must do IPAM)")
		}
		layer2 := map[string]any{"role": "Primary", "subnets": []any{p.Subnet}}
		udn, err := yaml.Marshal(map[string]any{
			"apiVersion": "k8s.ovn.org/v1",
			"kind":       "UserDefinedNetwork",
			"metadata": map[string]any{
				"name": p.Name, "namespace": s.Name,
				// Apply before any workload in this namespace.
				"annotations": map[string]any{"argocd.argoproj.io/sync-wave": "-1"},
			},
			"spec": map[string]any{"topology": "Layer2", "layer2": layer2},
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
	if s.Name == "" || s.NIC == "" {
		return "", nil, fmt.Errorf("an uplink name and a NIC are required")
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
