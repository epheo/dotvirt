package cluster

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/model"
)

func TestNetworkFromUDN(t *testing.T) {
	// A primary Layer2 UDN is the project's "VM Network" (default kind).
	primary := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "vm-network", "namespace": "tenant-a"},
		"spec": map[string]any{
			"topology": "Layer2",
			"layer2":   map[string]any{"role": "Primary", "subnets": []any{"10.0.0.0/24"}},
		},
	}}
	got := networkFromUDN(primary)
	want := model.Network{
		Name: "vm-network", Kind: model.NetworkDefault, Scope: model.ScopeProject,
		Namespace: "tenant-a", Subnets: []string{"10.0.0.0/24"}, Backing: "UserDefinedNetwork",
		Topology: "Layer2", AttachRef: "tenant-a/vm-network",
	}
	assertNetwork(t, got, want)

	// A secondary Layer2 UDN is an isolated internal port group.
	secondary := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "db-isolated", "namespace": "tenant-a"},
		"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Secondary"}},
	}}
	if k := networkFromUDN(secondary).Kind; k != model.NetworkInternal {
		t.Errorf("secondary UDN kind = %q, want %q", k, model.NetworkInternal)
	}
}

func TestNetworkFromCUDN_Localnet(t *testing.T) {
	// A localnet CUDN is a VLAN-backed shared port group; its config nests under
	// spec.network (not spec, as UDN does).
	cudn := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "prod-vlan-200"},
		"spec": map[string]any{
			"network": map[string]any{
				"topology": "Localnet",
				"localnet": map[string]any{
					"role":                "Secondary",
					"physicalNetworkName": "physnet-prod",
					"vlan":                map[string]any{"mode": "Access", "access": map[string]any{"id": int64(200)}},
				},
			},
		},
	}}
	got := networkFromCUDN(cudn)
	want := model.Network{
		Name: "prod-vlan-200", Kind: model.NetworkVLAN, Scope: model.ScopeShared,
		VLAN: 200, Uplink: "physnet-prod", Backing: "ClusterUserDefinedNetwork",
		Topology: "Localnet", AttachRef: "prod-vlan-200",
	}
	assertNetwork(t, got, want)
}

func TestNetworkFromNAD(t *testing.T) {
	// A raw localnet NAD: kind + VLAN come from the JSON CNI config string.
	nad := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "legacy-localnet", "namespace": "tenant-b"},
		"spec":     map[string]any{"config": `{"type":"ovn-k8s-cni-overlay","topology":"localnet","vlanID":42}`},
	}}
	got := networkFromNAD(nad)
	if got.Kind != model.NetworkVLAN || got.VLAN != 42 {
		t.Errorf("NAD decode = kind %q vlan %d, want vlan/42", got.Kind, got.VLAN)
	}
	if got.AttachRef != "tenant-b/legacy-localnet" {
		t.Errorf("NAD attachRef = %q", got.AttachRef)
	}
}

func TestAdaptersFromNNS(t *testing.T) {
	// Only real NICs (ethernet/bond) surface; derived devices (ovs-bridge) don't.
	// Role comes from the controller the NIC is enslaved to.
	nns := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "worker-1"},
		"status": map[string]any{"currentState": map[string]any{"interfaces": []any{
			map[string]any{"name": "eno1", "type": "ethernet", "state": "up", "mac-address": "aa:bb", "mtu": int64(1500), "controller": "br-ex"},
			map[string]any{"name": "eno2", "type": "ethernet", "state": "up"},
			map[string]any{"name": "br-ex", "type": "ovs-bridge", "state": "up"},
		}}},
	}}
	got := adaptersFromNNS(nns)
	if len(got) != 2 {
		t.Fatalf("got %d adapters, want 2 (ovs-bridge excluded): %+v", len(got), got)
	}
	if got[0].Node != "worker-1" || got[0].Role != "cluster-uplink" || got[0].MTU != 1500 {
		t.Errorf("eno1 = %+v, want node worker-1 / role cluster-uplink / mtu 1500", got[0])
	}
	if got[1].Role != "available" {
		t.Errorf("eno2 role = %q, want available", got[1].Role)
	}
}

func assertNetwork(t *testing.T, got, want model.Network) {
	t.Helper()
	if got.Name != want.Name || got.Kind != want.Kind || got.Scope != want.Scope ||
		got.Namespace != want.Namespace || got.VLAN != want.VLAN || got.Uplink != want.Uplink ||
		got.Backing != want.Backing || got.Topology != want.Topology || got.AttachRef != want.AttachRef {
		t.Errorf("network mismatch:\n got %+v\nwant %+v", got, want)
	}
	if len(got.Subnets) != len(want.Subnets) {
		t.Errorf("subnets = %v, want %v", got.Subnets, want.Subnets)
		return
	}
	for i := range want.Subnets {
		if got.Subnets[i] != want.Subnets[i] {
			t.Errorf("subnet[%d] = %q, want %q", i, got.Subnets[i], want.Subnets[i])
		}
	}
}
