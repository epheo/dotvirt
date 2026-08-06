package model

// Networks: the port-group abstraction over OVN-K.

// NetworkKind classifies a port group by how a virt admin reads it.
type NetworkKind string

const (
	NetworkDefault  NetworkKind = "default"  // primary network - the project's "VM Network"
	NetworkInternal NetworkKind = "internal" // Layer2, no uplink - an isolated port group
	NetworkVLAN     NetworkKind = "vlan"     // localnet - VLAN-backed, bridged to an uplink
)

// NetworkScope is a port group's reach: one project or shared across many.
type NetworkScope string

const (
	ScopeProject NetworkScope = "project" // namespace-scoped (UDN/NAD) - one project
	ScopeShared  NetworkScope = "shared"  // cluster-scoped (CUDN) - selected projects
)

// Network is one Distributed Port Group: a network a VM attaches a NIC to,
// abstracting a UDN, CUDN, or raw NAD behind port-group vocabulary.
type Network struct {
	Name      string       `json:"name"`                // the port-group name shown to the user
	Kind      NetworkKind  `json:"kind"`                // default | internal | vlan
	Scope     NetworkScope `json:"scope"`               // project | shared
	Namespace string       `json:"namespace,omitempty"` // for project-scoped (UDN/NAD)
	VLAN      int          `json:"vlan,omitempty"`      // 802.1q tag (vlan kind)
	Subnets   []string     `json:"subnets,omitempty"`   // CIDRs, when IPAM-managed
	Uplink    string       `json:"uplink,omitempty"`    // physicalNetworkName (vlan kind)
	// AttachRef is how a VM attaches: "namespace/nad". For a CUDN it's the bare
	// name - the generated NAD is namespace-relative, resolved at attach time (6.3).
	AttachRef string `json:"attachRef,omitempty"`
	Backing   string `json:"backing"`            // UserDefinedNetwork | ClusterUserDefinedNetwork | NetworkAttachmentDefinition
	Topology  string `json:"topology,omitempty"` // raw OVN-K topology (Layer2|Layer3|Localnet), for the detail drawer
	// Namespaces is where a shared (CUDN) network is actually attachable - the set
	// where it generated a NAD (its namespaceSelector's effective result). Empty
	// for project-scoped networks (those attach only in their own Namespace).
	Namespaces []string `json:"namespaces,omitempty"`

	// From ArgoCD, when enabled - the same per-object drift VMs carry, so a segment
	// that failed to apply (or is mid-sync) shows its own badge, not just its project's.
	// Empty when Argo isn't wired or no Application manages this object.
	Sync      SyncStatus `json:"sync,omitempty"`
	Health    string     `json:"health,omitempty"`
	SyncError string     `json:"syncError,omitempty"`
}

// Uplink is a physical-network attachment point - the vDS uplink analog: an OVN-K
// physical-network name mapped to an OVS bridge across a set of nodes. Builtin is
// the always-present br-ex default (no NNCP required).
type Uplink struct {
	Name      string   `json:"name"`              // physicalNetworkName
	Bridge    string   `json:"bridge"`            // OVS bridge (br-ex, br-physnet...)
	Builtin   bool     `json:"builtin,omitempty"` // the default br-ex uplink
	Nodes     []string `json:"nodes,omitempty"`   // nodes carrying the mapping
	NodeCount int      `json:"nodeCount"`         // len(Nodes), for the "N/M nodes" badge
	Ports     []string `json:"ports,omitempty"`   // physical NIC(s)/bond enslaved to the bridge
	VLANs     []int    `json:"vlans,omitempty"`   // LLDP-discovered VLAN IDs (6.5)
	Status    string   `json:"status,omitempty"`  // NNCE rollup: Available | Progressing | Failing (6.5)
}

// PhysicalAdapter is one node NIC from NodeNetworkState - the host "Physical
// adapters" view. Role says what the NIC is already doing.
type PhysicalAdapter struct {
	Name  string `json:"name"` // eno1, bond0...
	Node  string `json:"node"`
	Type  string `json:"type,omitempty"` // ethernet | bond
	MAC   string `json:"mac,omitempty"`
	State string `json:"state,omitempty"` // up | down
	MTU   int    `json:"mtu,omitempty"`
	Role  string `json:"role,omitempty"` // cluster-uplink | enslaved | available
}

// NetworkInventory is GET /api/networks: the port groups the caller may attach to,
// plus (for node-readers) the physical fabric. NMStatePresent=false means the
// NMState operator isn't installed, so uplink/adapter discovery is unavailable -
// the UI hides those affordances rather than showing empty panels.
type NetworkInventory struct {
	Networks         []Network         `json:"networks"`
	Uplinks          []Uplink          `json:"uplinks"`
	PhysicalAdapters []PhysicalAdapter `json:"physicalAdapters"`
	NMStatePresent   bool              `json:"nmstatePresent"`
	// CanManage is true when the caller may author platform-tier networking
	// (cluster-scoped CUDN): a platform repo is configured AND the caller passes the
	// CUDN-create SSAR. The coarse "any platform authoring" signal that gates the
	// platform-draft view; per-button gating uses Caps.
	CanManage bool `json:"canManage"`
	// Caps is the caller's per-action authoring authority - each field the same SSAR
	// the matching create handler enforces, so a button gated on its field can never
	// offer an action the backend would 403.
	Caps NetworkCaps `json:"caps"`
}

// NetworkCaps mirrors each platform-tier create handler's platformScope SSAR, so the
// UI can show only the authoring buttons the caller can actually use. All false when
// no platform repo is configured.
type NetworkCaps struct {
	SharedSegment      bool `json:"sharedSegment"`      // shared / VLAN CUDN
	Uplink             bool `json:"uplink"`             // nmstate NNCP
	Namespace          bool `json:"namespace"`          // namespaces (New Project / Namespace)
	EgressIP           bool `json:"egressIP"`           // Tier-0 SNAT
	ExternalRoute      bool `json:"externalRoute"`      // Tier-0 external route
	AdminNetworkPolicy bool `json:"adminNetworkPolicy"` // cluster-wide admin DFW (ANP/BANP)
}
