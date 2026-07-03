// Package model holds the API-facing types shared across dotvirt's planes.
package model

import "errors"

// Error kinds the domain (e.g. changeset) can wrap so the HTTP layer maps them to
// the right status instead of a blanket 500. Wrap with fmt.Errorf("%w: …", kind).
var (
	ErrInvalid     = errors.New("invalid request")         // → 400: bad/empty input, nothing to do
	ErrNotFound    = errors.New("not found")               // → 404
	ErrConflict    = errors.New("conflict")                // → 409: e.g. project not editable
	ErrUnavailable = errors.New("temporarily unavailable") // → 503: a capability isn't wired/reachable
)

// Power is the desired run state derived from a VM manifest's runStrategy.
type Power string

const (
	PowerOn      Power = "On"      // runStrategy Always / running: true
	PowerOff     Power = "Off"     // runStrategy Halted / running: false
	PowerUnknown Power = "Unknown" // unset / unrecognized
)

// SyncStatus mirrors ArgoCD's per-resource sync state.
type SyncStatus string

const (
	SyncSynced     SyncStatus = "Synced"
	SyncOutOfSync  SyncStatus = "OutOfSync"
	SyncNotTracked SyncStatus = "NotTracked" // no ArgoCD Application manages this VM
	SyncUnknown    SyncStatus = "Unknown"
)

// VM is a single virtual machine as shown in the inventory. Fields are populated
// from whichever planes are active: git always; cluster/argo when enabled.
type VM struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	// From git manifest (desired state).
	Power        Power             `json:"power"`
	CPUCores     int               `json:"cpuCores,omitempty"`
	Memory       string            `json:"memory,omitempty"` // e.g. "2Gi"
	Instancetype string            `json:"instancetype,omitempty"`
	Preference   string            `json:"preference,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Disks        []Disk            `json:"disks,omitempty"`
	Networks     []NIC             `json:"networks,omitempty"`
	SourceFile   string            `json:"sourceFile"` // path within the repo
	// DRSExclude: the descheduler prefer-no-eviction annotation is on the VM
	// template, so automatic rebalancing skips this VM (drains still migrate it).
	DRSExclude bool `json:"drsExclude,omitempty"`
	// EvictionStrategy is the template's explicit evictionStrategy (LiveMigrate,
	// None, ...); empty means the cluster default.
	EvictionStrategy string `json:"evictionStrategy,omitempty"`

	// From cluster (actual state), when cluster reads are enabled.
	Phase        string   `json:"phase,omitempty"`  // VMI phase, e.g. Running
	Paused       bool     `json:"paused,omitempty"` // VMI Paused condition (phase stays Running)
	GuestIP      string   `json:"guestIP,omitempty"`
	IPs          []string `json:"ips,omitempty"` // every guest-reported IP
	NodeName     string   `json:"nodeName,omitempty"`
	OS           string   `json:"os,omitempty"`           // guest-agent OS pretty name
	MemoryActual string   `json:"memoryActual,omitempty"` // current guest memory (hotplug-aware)
	StartedAt    string   `json:"startedAt,omitempty"`    // RFC3339; VMI entered Running (for uptime)

	// Migration is the live (or last) node-to-node move — vCenter's vMotion
	// progress, read from the VMI's migration state. Nil when never migrated.
	Migration *Migration `json:"migration,omitempty"`

	// From ArgoCD, when enabled.
	Sync   SyncStatus `json:"sync"`
	Health string     `json:"health,omitempty"`
	// SyncError is ArgoCD's apply failure for this VM (e.g. a webhook rejection),
	// surfaced so the UI can explain an OutOfSync VM instead of just flagging it.
	SyncError string `json:"syncError,omitempty"`
}

// Migration mirrors the VMI's migration state. Active while neither Completed
// nor Failed is set.
type Migration struct {
	SourceNode string `json:"sourceNode,omitempty"`
	TargetNode string `json:"targetNode,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"` // RFC3339
	EndedAt    string `json:"endedAt,omitempty"`   // RFC3339
	Completed  bool   `json:"completed,omitempty"`
	Failed     bool   `json:"failed,omitempty"`
}

// Disk is a disk device on the VM (from the template).
type Disk struct {
	Name         string `json:"name"`
	Type         string `json:"type,omitempty"`         // dataVolume | emptyDisk | containerDisk | cloudInitNoCloud | …
	Size         string `json:"size,omitempty"`         // emptyDisk capacity or dataVolume requested storage
	StorageClass string `json:"storageClass,omitempty"` // dataVolume storageClassName (empty = cluster default)
}

// NIC is a network interface on the VM. Name/Network come from the manifest;
// MAC/IP are merged from the live VMI status when running.
type NIC struct {
	Name    string `json:"name"`
	Network string `json:"network,omitempty"` // "pod" or the multus networkName
	MAC     string `json:"mac,omitempty"`     // live, from VMI status
	IP      string `json:"ip,omitempty"`      // live, from VMI status
}

// ProjectNamespace is one namespace bucket within a project: the VMs it holds.
type ProjectNamespace struct {
	Namespace string `json:"namespace"`
	VMs       []VM   `json:"vms"`
}

// Project is a tenant in the vCenter-style inventory tree: a named set of
// namespaces backed by one git repo. Name + Repo come from namespace
// label/annotation (dotvirt.io/project, dotvirt.io/repo). Error is set (and Repo
// left empty) when a project's namespaces are labeled but have no usable repo —
// surfaced as a warning in the UI rather than failing the whole inventory.
type Project struct {
	Name       string             `json:"name"`
	Repo       string             `json:"repo,omitempty"`
	Namespaces []ProjectNamespace `json:"namespaces"`
	Error      string             `json:"error,omitempty"`
}

// Inventory is the full multi-project tree. Warnings carry non-fatal degradations
// (e.g. live or drift state couldn't be read) so the UI can say "status
// unavailable" instead of silently rendering every VM as stopped / not-tracked.
// Proposals rides along so the open-PR lane updates over the live stream — a PR
// merged anywhere (the git poll sees main move) repaints it with no client poll.
type Inventory struct {
	Projects  []Project  `json:"projects"`
	Warnings  []string   `json:"warnings,omitempty"`
	Proposals []Proposal `json:"proposals,omitempty"`
}

// Change is one human-readable, YAML-free change item (a semantic diff entry).
// Action is "change" (From→To), "add" (To), or "remove" (From).
type Change struct {
	Field  string `json:"field"`
	Action string `json:"action"` // change | add | remove
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// --- DTOs crossing the API boundary ---

// EditRequest is the body of an edit: which VM source file, and which fields to
// change. Power is "On"/"Off"; nil fields are left unchanged.
type EditRequest struct {
	SourceFile   string  `json:"sourceFile"`
	Power        *string `json:"power,omitempty"`
	CPUCores     *int    `json:"cpuCores,omitempty"`
	Memory       *string `json:"memory,omitempty"`
	Instancetype *string `json:"instancetype,omitempty"`
	Preference   *string `json:"preference,omitempty"`
	Sizing       *string `json:"sizing,omitempty"` // "instancetype" | "custom" — which representation owns CPU/memory

	SetLabels        map[string]string `json:"setLabels,omitempty"`
	RemoveLabels     []string          `json:"removeLabels,omitempty"`
	DRSExclude       *bool             `json:"drsExclude,omitempty"`       // toggle the descheduler prefer-no-eviction annotation
	EvictionStrategy *string           `json:"evictionStrategy,omitempty"` // "" removes (cluster default)
	AddDisks         []DiskAdd         `json:"addDisks,omitempty"`
	RemoveDisks      []string          `json:"removeDisks,omitempty"`
	AddNetworks      []NetworkAdd      `json:"addNetworks,omitempty"`
	RemoveNetworks   []string          `json:"removeNetworks,omitempty"`

	Message string `json:"message,omitempty"` // optional commit message; auto-generated when empty
}

// DiskAdd / NetworkAdd are the add-device entries in an EditRequest body.
type DiskAdd struct {
	Name string `json:"name"`
	Size string `json:"size"`
}
type NetworkAdd struct {
	Name string `json:"name"`
}

// ProposeRequest is the body of a propose: PR title + description.
type ProposeRequest struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

// DriftResult is a VM's drift (running vs main) as semantic changes.
type DriftResult struct {
	Drift   bool     `json:"drift"`
	Changes []Change `json:"changes"`
}

// DraftItem is one pending change rendered for the UI.
type DraftItem struct {
	Kind      string   `json:"kind"`               // edit | create | delete
	Resource  string   `json:"resource,omitempty"` // "" == vm | network — disambiguates unstage
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Changes   []Change `json:"changes"`
	YAML      string   `json:"yaml,omitempty"` // raw/edited manifest for the collapsed view
}

// DraftView is the whole draft changeset as semantic items.
type DraftView struct {
	Base   string      `json:"base"`
	Branch string      `json:"branch"`
	Count  int         `json:"count"`
	Items  []DraftItem `json:"items"`
}

// ProposeResult is returned after proposing the draft as a PR.
type ProposeResult struct {
	Branch     string `json:"branch"`
	Pushed     bool   `json:"pushed"`
	PRURL      string `json:"prURL,omitempty"`
	PRNumber   int    `json:"prNumber,omitempty"`
	CompareURL string `json:"compareURL,omitempty"`
	Existing   bool   `json:"existing,omitempty"`
}

// Proposal is an open pull request backing a project's draft — the staged→PR→
// synced lifecycle's middle state, surfaced as a Recent Tasks row.
type Proposal struct {
	Project  string `json:"project"`
	PRNumber int    `json:"prNumber"`
	PRURL    string `json:"prURL"`
	Title    string `json:"title,omitempty"`
}

// Permissions is the caller's effective capability set in one namespace — the
// Permissions tab. Curated to what the UI does under the user's token; config/
// power/delete are PR-gated (the forge decides), so they aren't rows here.
type Permissions struct {
	Namespace    string       `json:"namespace"`
	Capabilities []Capability `json:"capabilities"`
	Incomplete   bool         `json:"incomplete,omitempty"` // the rules review couldn't enumerate everything
}

// Capability is one Permissions row: a UI action and whether the caller's token
// may perform it, with the RBAC behind it for the tooltip.
type Capability struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Allowed bool   `json:"allowed"`
	Detail  string `json:"detail,omitempty"`
}

// Commit is one entry in a project's git history, shown in the Changes pane.
type Commit struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"shortHash"`
	Message   string `json:"message"`
	Author    string `json:"author"`
	When      string `json:"when"`            // RFC3339
	Merge     bool   `json:"merge,omitempty"` // a merge commit (not directly revertable)
}

// --- Performance metrics (the VM Performance tab) ---

// MetricSeries is one line in a chart: a value per timestamp in the parent
// MetricChart's Times grid (nil = a gap, no sample at that time).
type MetricSeries struct {
	Name   string     `json:"name"`
	Values []*float64 `json:"values"`
}

// MetricChart is one performance chart: a shared time axis plus its series, with a
// unit hint the UI formats by ("%", "bytes", "Bps", "iops", "ms").
type MetricChart struct {
	Key     string         `json:"key"`
	Title   string         `json:"title"`
	Unit    string         `json:"unit"`
	Stacked bool           `json:"stacked,omitempty"` // series partition a whole; render as stacked area
	Times   []int64        `json:"times"`             // unix seconds, the shared x-axis
	Series  []MetricSeries `json:"series"`
}

// VMMetrics is a VM's performance time-series for one range — several charts built
// from KubeVirt's kubevirt_vmi_* Prometheus metrics, shaped for direct charting.
type VMMetrics struct {
	Range   string        `json:"range"`
	StepSec int           `json:"stepSec"`
	Charts  []MetricChart `json:"charts"`
}

// --- Capacity & usage (Summary "Capacity and Usage" widgets) ---

// UsageMetric is one resource's point-in-time usage for a VM Summary bar — Used of
// Total in the same unit, with a short recent history for an inline sparkline.
type UsageMetric struct {
	Used  float64   `json:"used"`
	Total float64   `json:"total,omitempty"` // 0 ⇒ no known denominator (show the value alone)
	Spark []float64 `json:"spark,omitempty"`
}

// VMUsage is a VM's live capacity-and-usage for the Summary tab (vCenter's
// "Capacity and Usage" panel): CPU % of allocated, memory used of allocated,
// guest-filesystem used of provisioned.
type VMUsage struct {
	Updated int64       `json:"updated"` // unix seconds ("Last updated")
	CPU     UsageMetric `json:"cpu"`     // Used = % of allocated vCPU, Total = 100
	Memory  UsageMetric `json:"memory"`  // bytes; Total = allocated (domain)
	Storage UsageMetric `json:"storage"` // bytes; guest filesystem used / capacity
}

// ClusterMetric is one aggregate resource for the cluster/infrastructure rings:
// Used now, Allocated (committed to VMs), of Total (node-allocatable capacity).
type ClusterMetric struct {
	Used      float64   `json:"used"`
	Allocated float64   `json:"allocated,omitempty"` // committed to VMs (vCPU / declared memory)
	Total     float64   `json:"total"`               // node-allocatable capacity (the boundary)
	Spark     []float64 `json:"spark,omitempty"`
}

// ConsumerVM is one row in a "top consumers" list (a VM ranked by a resource).
type ConsumerVM struct {
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	Value     float64 `json:"value"`
}

// ClusterSummary is the aggregate capacity view for the "All VMs" landing — the
// vCenter cluster-Summary analog: rings (used vs node-allocatable) + VM counts by
// phase + top-consumer VMs. VM-scoped sums are limited to the caller's namespaces;
// node capacity is the cluster-wide boundary.
type ClusterSummary struct {
	Updated   int64          `json:"updated"`
	CPU       ClusterMetric  `json:"cpu"`     // cores
	Memory    ClusterMetric  `json:"memory"`  // bytes
	Storage   ClusterMetric  `json:"storage"` // bytes
	VMs       map[string]int `json:"vms"`     // phase → count
	TopCPU    []ConsumerVM   `json:"topCpu"`
	TopMemory []ConsumerVM   `json:"topMemory"`
}

// QuotaItem is one resource row of a ResourceQuota: current usage against the
// hard cap, pre-parsed for direct charting.
type QuotaItem struct {
	Resource string  `json:"resource"` // e.g. requests.cpu, requests.memory
	Used     float64 `json:"used"`
	Hard     float64 `json:"hard"`
	Unit     string  `json:"unit"` // cores | bytes | count
}

// NamespaceQuota is one ResourceQuota in one namespace — the project capacity
// band's input. A namespace may carry several (scoped) quotas.
type NamespaceQuota struct {
	Namespace string      `json:"namespace"`
	Name      string      `json:"name"`
	Items     []QuotaItem `json:"items"`
}

// Alert is one firing Prometheus alert (the dock's Alarms tab). VM is set when
// the alert's series carries a name label (kubevirt_vmi_* alerts do); Count
// collapses identical (name, severity, namespace, vm) series.
type Alert struct {
	Name      string `json:"name"`
	Severity  string `json:"severity,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	VM        string `json:"vm,omitempty"`
	Count     int    `json:"count,omitempty"`
}

// Event is a Kubernetes Event for a VM (or its VMI), shown in the Monitor tab and
// the dock's Events lane (which uses Namespace/Name to label which VM it's about).
type Event struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Type      string `json:"type"` // Normal | Warning
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Count     int32  `json:"count,omitempty"`
	Object    string `json:"object"`             // VirtualMachine | VirtualMachineInstance
	LastSeen  string `json:"lastSeen,omitempty"` // RFC3339
}

// Snapshot is a VirtualMachineSnapshot for a VM — the Snapshots tab. KubeVirt
// snapshots are a flat list (no vCenter-style parent/child tree).
type Snapshot struct {
	Name        string   `json:"name"`
	Created     string   `json:"created,omitempty"` // RFC3339
	Phase       string   `json:"phase,omitempty"`   // InProgress | Succeeded | Failed
	ReadyToUse  bool     `json:"readyToUse"`
	Indications []string `json:"indications,omitempty"` // Online | GuestAgent | NoGuestAgent
	Error       string   `json:"error,omitempty"`
}

// Clone is a VirtualMachineClone whose source is a VM — one row in the Clone
// flow's progress list. The clone controller snapshots the source and restores
// it into the target VM; the target exists only in the cluster (NotTracked)
// until adopted into git.
type Clone struct {
	Name    string `json:"name"`
	Target  string `json:"target"`
	Phase   string `json:"phase,omitempty"`   // SnapshotInProgress | RestoreInProgress | CreatingTargetVM | Succeeded | Failed | …
	Created string `json:"created,omitempty"` // RFC3339
}

// UploadStatus is an upload DataVolume's progress (the image-upload flow). Ready
// (phase UploadReady) means cdi-uploadproxy will accept the bytes; Progress is
// CDI's import-progress percentage once they're flowing.
type UploadStatus struct {
	Phase    string `json:"phase"`
	Ready    bool   `json:"ready"`
	Progress string `json:"progress,omitempty"`
}

// UploadToken is the bearer + endpoint the browser POSTs the image to, streaming
// directly to cdi-uploadproxy (which ships open CORS).
type UploadToken struct {
	Token     string `json:"token"`
	UploadURL string `json:"uploadUrl"`
}

// UploadTarget identifies the upload DataVolume just created.
type UploadTarget struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// NodeInfo is a node's maintenance state for the By-Node view: whether it's
// cordoned, and whether the caller's token may cordon it (so the UI hides the
// action for users without node-update RBAC).
type NodeInfo struct {
	Name          string `json:"name"`
	Unschedulable bool   `json:"unschedulable"`
	CanCordon     bool   `json:"canCordon"`
}

// ResyncResult reports which ArgoCD Application was synced.
type ResyncResult struct {
	Application string `json:"application"`
	Revision    string `json:"revision"`
}

// Options are the cluster-provided choices for the wizard/editor.
type Options struct {
	Instancetypes  []Instancetype  `json:"instancetypes"`
	Preferences    []Preference    `json:"preferences"`
	OSImages       []OSImage       `json:"osImages"`
	Networks       []NetworkOption `json:"networks"`
	StorageClasses []StorageClass  `json:"storageClasses"`
}

type Instancetype struct {
	Name   string `json:"name"`
	CPU    int64  `json:"cpu"`
	Memory string `json:"memory"`
}
type Preference struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
}
type OSImage struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Ready     bool   `json:"ready"`
}
type NetworkOption struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}
type StorageClass struct {
	Name    string `json:"name"`
	Default bool   `json:"default,omitempty"` // the cluster's default class annotation
}

// --- Networks (the vCenter "Distributed Port Group" abstraction) ---
//
// dotvirt presents OVN-K networking in VMware terms: a Network is a port group a
// VM NIC attaches to; an Uplink is the physical-adapter binding (the vDS uplink);
// a PhysicalAdapter is one node NIC. The OVN-K objects behind them (UDN, CUDN,
// localnet, NAD) and nmstate (NNCP, NNS) never surface to the user.

// NetworkKind classifies a port group by how a VMware admin reads it.
type NetworkKind string

const (
	NetworkDefault  NetworkKind = "default"  // primary network — the project's "VM Network"
	NetworkInternal NetworkKind = "internal" // Layer2, no uplink — an isolated port group
	NetworkVLAN     NetworkKind = "vlan"     // localnet — VLAN-backed, bridged to an uplink
)

// NetworkScope is a port group's reach: one project or shared across many.
type NetworkScope string

const (
	ScopeProject NetworkScope = "project" // namespace-scoped (UDN/NAD) — one project
	ScopeShared  NetworkScope = "shared"  // cluster-scoped (CUDN) — selected projects
)

// Network is one Distributed Port Group: a network a VM attaches a NIC to,
// abstracting a UDN, CUDN, or raw NAD behind vCenter vocabulary.
type Network struct {
	Name      string       `json:"name"`                // the port-group name shown to the user
	Kind      NetworkKind  `json:"kind"`                // default | internal | vlan
	Scope     NetworkScope `json:"scope"`               // project | shared
	Namespace string       `json:"namespace,omitempty"` // for project-scoped (UDN/NAD)
	VLAN      int          `json:"vlan,omitempty"`      // 802.1q tag (vlan kind)
	Subnets   []string     `json:"subnets,omitempty"`   // CIDRs, when IPAM-managed
	Uplink    string       `json:"uplink,omitempty"`    // physicalNetworkName (vlan kind)
	// AttachRef is how a VM attaches: "namespace/nad". For a CUDN it's the bare
	// name — the generated NAD is namespace-relative, resolved at attach time (6.3).
	AttachRef string `json:"attachRef,omitempty"`
	Backing   string `json:"backing"`            // UserDefinedNetwork | ClusterUserDefinedNetwork | NetworkAttachmentDefinition
	Topology  string `json:"topology,omitempty"` // raw OVN-K topology (Layer2|Layer3|Localnet), for the detail drawer
	// Namespaces is where a shared (CUDN) network is actually attachable — the set
	// where it generated a NAD (its namespaceSelector's effective result). Empty
	// for project-scoped networks (those attach only in their own Namespace).
	Namespaces []string `json:"namespaces,omitempty"`
}

// Uplink is a physical-network attachment point — the vDS uplink analog: an OVN-K
// physical-network name mapped to an OVS bridge across a set of nodes. Builtin is
// the always-present br-ex default (no NNCP required).
type Uplink struct {
	Name      string   `json:"name"`              // physicalNetworkName
	Bridge    string   `json:"bridge"`            // OVS bridge (br-ex, br-physnet…)
	Builtin   bool     `json:"builtin,omitempty"` // the default br-ex uplink
	Nodes     []string `json:"nodes,omitempty"`   // nodes carrying the mapping
	NodeCount int      `json:"nodeCount"`         // len(Nodes), for the "N/M nodes" badge
	Ports     []string `json:"ports,omitempty"`   // physical NIC(s)/bond enslaved to the bridge
	VLANs     []int    `json:"vlans,omitempty"`   // LLDP-discovered VLAN IDs (6.5)
	Status    string   `json:"status,omitempty"`  // NNCE rollup: Available | Progressing | Failing (6.5)
}

// PhysicalAdapter is one node NIC from NodeNetworkState — the host "Physical
// adapters" view. Role says what the NIC is already doing.
type PhysicalAdapter struct {
	Name  string `json:"name"` // eno1, bond0…
	Node  string `json:"node"`
	Type  string `json:"type,omitempty"` // ethernet | bond
	MAC   string `json:"mac,omitempty"`
	State string `json:"state,omitempty"` // up | down
	MTU   int    `json:"mtu,omitempty"`
	Role  string `json:"role,omitempty"` // cluster-uplink | enslaved | available
}

// NetworkInventory is GET /api/networks: the port groups the caller may attach to,
// plus (for node-readers) the physical fabric. NMStatePresent=false means the
// NMState operator isn't installed, so uplink/adapter discovery is unavailable —
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
	// Caps is the caller's per-action authoring authority — each field the same SSAR
	// the matching create handler enforces, so a button gated on its field can never
	// offer an action the backend would 403.
	Caps NetworkCaps `json:"caps"`
}

// DRSConfig is the committed DRS configuration, parsed back from the platform
// repo's KubeDescheduler manifest (defaults resolved).
type DRSConfig struct {
	Mode               string `json:"mode"`      // Predictive | Automatic
	Threshold          string `json:"threshold"` // AsymmetricLow | Low | Medium | High
	IntervalSeconds    int    `json:"intervalSeconds"`
	SoftTainter        bool   `json:"softTainter"`
	EvictionNodeLimit  int    `json:"evictionNodeLimit"`
	EvictionTotalLimit int    `json:"evictionTotalLimit"`
}

// DRSGitState is the platform repo's committed DRS state on the base branch.
type DRSGitState struct {
	Configured    bool       `json:"configured"`       // the KubeDescheduler CR is committed
	Config        *DRSConfig `json:"config,omitempty"` // nil when the committed CR doesn't parse (hand-edited)
	PSIConfigured bool       `json:"psiConfigured"`    // the PSI MachineConfig is committed
}

// DRSDraftState is the caller's pending (staged, not yet proposed) DRS change
// — the plane between committed and live that the panel's dialog edits.
type DRSDraftState struct {
	Config        *DRSConfig `json:"config,omitempty"` // the staged KubeDescheduler spec
	PSI           bool       `json:"psi,omitempty"`    // the PSI MachineConfig is staged too
	DisableStaged bool       `json:"disableStaged,omitempty"`
}

// DRSLive is the descheduler's live state, read from the SA-watched
// KubeDescheduler snapshot — never the cluster per-request.
type DRSLive struct {
	// APIPresent: the Kube Descheduler Operator's CRD is served. False on a
	// cluster where the operator was never installed — the "not installed" state
	// the panel shows until the first enable-PR merges and OLM installs it.
	APIPresent bool `json:"apiPresent"`
	// Synced: the initial LIST landed; until then Deployed=false means
	// "unknown", not "absent". Stale: the API is served but the watch is
	// currently failing (e.g. RBAC not yet reconciled, apiserver outage) — the
	// live fields may be missing or outdated.
	Synced bool `json:"synced"`
	Stale  bool `json:"stale,omitempty"`
	// Deployed: a KubeDescheduler CR exists in the cluster.
	Deployed        bool     `json:"deployed"`
	ManagementState string   `json:"managementState,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	Profiles        []string `json:"profiles,omitempty"`
	IntervalSeconds int64    `json:"intervalSeconds,omitempty"`
	// Available mirrors the operator's Available condition; Degraded carries the
	// Degraded condition's message when that condition is true.
	Available bool   `json:"available"`
	Degraded  string `json:"degraded,omitempty"`
}

// DRSView is GET /api/drs: the DRS tier across its planes — the committed git
// state (flattened), the caller's staged draft, the live operator state — plus
// the caller's authoring capability, the same SSARs the POST/DELETE handlers
// enforce. Warning carries a non-fatal degradation (e.g. the platform repo is
// unreachable, so the committed state is unknown) instead of failing the view.
type DRSView struct {
	DRSGitState
	Draft     *DRSDraftState `json:"draft,omitempty"`
	Live      DRSLive        `json:"live"`
	Warning   string         `json:"warning,omitempty"`
	CanManage bool           `json:"canManage"` // kubedeschedulers-create — gates the panel's actions
	CanPSI    bool           `json:"canPSI"`    // machineconfigs-create — gates the PSI checkbox
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

// Template is one VirtualMachineTemplate manifest in a library repo's
// templates/ directory — a content-library entry (vSphere: a VM template).
// Name is the file's basename: the deployable identity the API routes carry.
type Template struct {
	Name         string              `json:"name"`
	Library      string              `json:"library"` // owning project, or "platform" (the shared library)
	Description  string              `json:"description,omitempty"`
	SourceFile   string              `json:"sourceFile"` // templates/<name>.yaml
	Parameters   []TemplateParameter `json:"parameters,omitempty"`
	Instancetype string              `json:"instancetype,omitempty"` // blueprint summary, best-effort
	Preference   string              `json:"preference,omitempty"`
	YAML         string              `json:"yaml"`
	Error        string              `json:"error,omitempty"` // parse failure — listed, but not deployable
}

// TemplateParameter mirrors template.kubevirt.io/v1beta1 Parameter, so the
// wizard's form is exactly what the native CRD will accept later.
type TemplateParameter struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Value       string `json:"value,omitempty"`
	Generate    string `json:"generate,omitempty"` // "expression" — value generated from From
	From        string `json:"from,omitempty"`     // the generator's input pattern
	Required    bool   `json:"required,omitempty"`
}

// TemplateList is the content-library listing across the caller's libraries.
type TemplateList struct {
	Templates []Template `json:"templates"`
}

// DeployTemplateRequest renders a library template and stages the resulting VM
// into the target namespace's project draft.
type DeployTemplateRequest struct {
	Library    string            `json:"library"`
	Template   string            `json:"template"`
	Namespace  string            `json:"namespace"`
	Name       string            `json:"name,omitempty"` // overrides the NAME parameter; empty → template default (often generated)
	Parameters map[string]string `json:"parameters,omitempty"`
	PowerOn    bool              `json:"powerOn,omitempty"` // boot the VM once it syncs (templates blueprint Halted)
}

// UpdateTemplateRequest replaces a library template's manifest — editing a
// content-library item. The file updates when the library's PR merges.
type UpdateTemplateRequest struct {
	Library string `json:"library"`
	Name    string `json:"name"`
	YAML    string `json:"yaml"`
}

// SaveTemplateRequest derives a template from an existing VM's git manifest and
// stages it into the chosen library ("Clone to Template").
type SaveTemplateRequest struct {
	Library         string `json:"library"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	SourceNamespace string `json:"sourceNamespace"`
	SourceName      string `json:"sourceName"`
}
