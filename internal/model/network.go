package model

// dotvirt presents OVN-K networking in plain network terms: a Network is a network
// a VM NIC attaches to, an Uplink is the physical-adapter binding, and a
// PhysicalAdapter is one node NIC. The OVN-K objects behind them (UDN, CUDN,
// localnet, NAD) and nmstate (NNCP, NNS) never surface to the user.

// NetworkKind classifies a network by how a virt admin reads it.
type NetworkKind string

const (
	NetworkDefault  NetworkKind = "default"  // primary network - the project's "VM Network"
	NetworkInternal NetworkKind = "internal" // Layer2, no uplink - an isolated network
	NetworkVLAN     NetworkKind = "vlan"     // localnet - VLAN-backed, bridged to an uplink
)

// NetworkScope is a network's reach: one project or shared across many.
type NetworkScope string

const (
	ScopeProject NetworkScope = "project" // namespace-scoped (UDN/NAD) - one project
	ScopeShared  NetworkScope = "shared"  // cluster-scoped (CUDN) - selected projects
)

// Network is one network a VM attaches a NIC to, abstracting a UDN, CUDN,
// or raw NAD behind one vocabulary.
type Network struct {
	Name      string       `json:"name"`                // the network name shown to the user
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

	// SourceFile is the manifest declaring this network in its project's repo
	// (the platform repo for a shared one); empty when git does not declare it,
	// which is what makes it editable and deletable from the UI.
	SourceFile string `json:"sourceFile,omitempty"`

	// From ArgoCD, when enabled - the same per-object drift VMs carry, so a network
	// that failed to apply (or is mid-sync) shows its own badge, not just its project's.
	// Empty when Argo isn't wired or no Application manages this object.
	Sync      SyncStatus `json:"sync,omitempty"`
	Health    string     `json:"health,omitempty"`
	SyncError string     `json:"syncError,omitempty"`
}

// Uplink is a physical-network attachment point - an OVN-K
// physical-network name mapped to an OVS bridge across a set of nodes. Builtin is
// the always-present br-ex default (no NNCP required).
type Uplink struct {
	Name      string   `json:"name"`              // physicalNetworkName
	Bridge    string   `json:"bridge"`            // OVS bridge (br-ex, br-physnet...)
	Builtin   bool     `json:"builtin,omitempty"` // the default br-ex uplink
	Nodes     []string `json:"nodes"`             // nodes carrying the mapping; never omitted, the web reads absent as every node
	NodeCount int      `json:"nodeCount"`         // len(Nodes), for the "N/M nodes" badge
	// Policy is the NodeNetworkConfigurationPolicy declaring this uplink - the
	// identity edit, delete and adoption act on. Empty for the builtin uplink and
	// when several policies fold into one uplink (no single object to act on).
	Policy string `json:"policy,omitempty"`
	// SourceFile is the manifest declaring that policy in the platform repo;
	// empty when git does not declare it.
	SourceFile string `json:"sourceFile,omitempty"`
	// Failures are the hosts where the policy did not apply, from nmstate's
	// per-host enactments: the uplink is missing there.
	Failures []UplinkFailure `json:"failures,omitempty"`
	// The policy's own ArgoCD drift, as networks and rules carry it.
	Sync      SyncStatus `json:"sync,omitempty"`
	Health    string     `json:"health,omitempty"`
	SyncError string     `json:"syncError,omitempty"`
}

// UplinkFailure is one host where an uplink's policy failed or was aborted.
type UplinkFailure struct {
	Node    string `json:"node"`
	Reason  string `json:"reason"`            // FailedToConfigure, ConfigurationAborted...
	Message string `json:"message,omitempty"` // the first line of nmstate's report
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

// NetworkInventory is GET /api/networks: the networks the caller may attach to,
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
	Uplink             bool `json:"uplink"`             // nmstate NNCP
	Namespace          bool `json:"namespace"`          // namespaces (New Project / Namespace)
	EgressIP           bool `json:"egressIP"`           // fixed outbound IP
	ExternalRoute      bool `json:"externalRoute"`      // external route
	AdminNetworkPolicy bool `json:"adminNetworkPolicy"` // cluster rules (AdminNetworkPolicy)
	// BaselineAdminNetworkPolicy is the cluster's baseline default policy.
	BaselineAdminNetworkPolicy bool `json:"baselineAdminNetworkPolicy"`
	// Platform is ClusterCaps.PlatformRead: the platform tier's shared reads
	// (its history, proposals and revert).
	Platform bool `json:"platform"`
}

// Gateways is GET /api/networking/gateways: every Gateway and route. Operator
// only: it reaches across every tenant's namespaces.
type Gateways struct {
	// Synced is false until the Gateways' and HTTPRoutes' first lists landed.
	Synced bool `json:"synced"`
	// Stale: a watch is down and this is its last known state.
	Stale    bool          `json:"stale,omitempty"`
	Gateways []GatewayInfo `json:"gateways"`
	Routes   []Route       `json:"routes"`
}

// GatewayInfo is one Gateway as its controller reports it.
type GatewayInfo struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Class     string   `json:"class"`
	Addresses []string `json:"addresses,omitempty"`
	Ready     bool     `json:"ready"`
	// Reason and Message are the first condition that does not hold.
	Reason     string             `json:"reason,omitempty"`
	Message    string             `json:"message,omitempty"`
	Listeners  []GatewayListener  `json:"listeners"`
	RateLimits []GatewayRateLimit `json:"rateLimits,omitempty"`
}

// GatewayListener is one port a Gateway serves.
type GatewayListener struct {
	Name           string `json:"name"`
	Port           int32  `json:"port"`
	Protocol       string `json:"protocol"`
	Hostname       string `json:"hostname,omitempty"`
	AttachedRoutes int32  `json:"attachedRoutes"`
	Ready          bool   `json:"ready"`
	Reason         string `json:"reason,omitempty"`
	Message        string `json:"message,omitempty"`
}

// GatewayRateLimit is one per-client budget on a Gateway; Listener is empty
// for the whole Gateway.
type GatewayRateLimit struct {
	Listener          string `json:"listener,omitempty"`
	RequestsPerSecond int32  `json:"requestsPerSecond"`
	Burst             int32  `json:"burst"`
}

// Route is one HTTP, TLS, TCP or UDP route.
type Route struct {
	Kind      string         `json:"kind"`
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Hostnames []string       `json:"hostnames,omitempty"`
	Parents   []RouteParent  `json:"parents,omitempty"`
	Backends  []RouteBackend `json:"backends,omitempty"`
}

// RouteParent is a Gateway a route asks to attach to.
type RouteParent struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Section   string `json:"section,omitempty"`
	Attached  bool   `json:"attached"`
	Reason    string `json:"reason,omitempty"`
	// Message is the controller's text, which can name another tenant's route.
	// Empty on GET /api/networking/routes.
	Message string `json:"message,omitempty"`
}

// RouteBackend is a Service a route sends traffic to.
type RouteBackend struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      int32  `json:"port,omitempty"`
	Weight    int32  `json:"weight"`
}
