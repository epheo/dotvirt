package netgen

import (
	"fmt"

	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/validate"
)

// UplinkSpec describes a physical-network attachment to create: an OVS bridge
// enslaving a NIC, mapped to a localnet physical-network name, across a set of
// nodes - the vDS-uplink analog, as an nmstate NodeNetworkConfigurationPolicy.
type UplinkSpec struct {
	Name         string            `json:"name"`                   // physical-network name (the localnet mapping)
	Bridge       string            `json:"bridge,omitempty"`       // OVS bridge to create; default br-<name>
	NIC          string            `json:"nic"`                    // physical port to enslave
	NodeSelector map[string]string `json:"nodeSelector,omitempty"` // node subset; empty = all worker nodes
}

// UplinkPolicyName is the NNCP an uplink renders as: the object's identity in
// git and on the cluster, distinct from the physical-network name it maps.
func UplinkPolicyName(name string) string { return "uplink-" + name }

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
		"metadata":   map[string]any{"name": UplinkPolicyName(s.Name)},
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
