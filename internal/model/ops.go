package model

// Imperative cluster operations and detail reads: snapshots, clones, uploads,
// nodes, events, and the wizard catalog.

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

// Snapshot is a VirtualMachineSnapshot for a VM - the Snapshots tab. KubeVirt
// snapshots are a flat list (no vCenter-style parent/child tree).
type Snapshot struct {
	Name        string   `json:"name"`
	Created     string   `json:"created,omitempty"` // RFC3339
	Phase       string   `json:"phase,omitempty"`   // InProgress | Succeeded | Failed
	ReadyToUse  bool     `json:"readyToUse"`
	Indications []string `json:"indications,omitempty"` // Online | GuestAgent | NoGuestAgent
	Error       string   `json:"error,omitempty"`
}

// Clone is a VirtualMachineClone whose source is a VM - one row in the Clone
// flow's progress list. The clone controller snapshots the source and restores
// it into the target VM; the target exists only in the cluster (NotTracked)
// until adopted into git.
type Clone struct {
	Name    string `json:"name"`
	Target  string `json:"target"`
	Phase   string `json:"phase,omitempty"`   // SnapshotInProgress | RestoreInProgress | CreatingTargetVM | Succeeded | Failed | ...
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

// Node is one virtualization host - a node KubeVirt marks schedulable for
// VMs - as a candidate live-migration target. Ready/Unschedulable let the
// target picker gray out hosts a migration could not land on.
type Node struct {
	Name          string `json:"name"`
	Ready         bool   `json:"ready"`
	Unschedulable bool   `json:"unschedulable,omitempty"`
	Maintenance   bool   `json:"maintenance,omitempty"`
}

// NodeInfo is a node's maintenance state for the By-Node view: whether it's
// cordoned, in maintenance mode, and whether the caller's token may cordon it
// (so the UI hides the actions for users without node-update RBAC).
// Maintenance is dotvirt's annotation-backed intent marker: it stays set until
// explicitly exited, even if something else uncordons the node underneath.
type NodeInfo struct {
	Name          string `json:"name"`
	Unschedulable bool   `json:"unschedulable"`
	Maintenance   bool   `json:"maintenance"`
	CanCordon     bool   `json:"canCordon"`
}

// Evacuation is one node-drain sweep's outcome. Failures carry per-VM errors:
// each migrate runs under the caller's own RBAC, so a partial sweep must say
// which VMs it could not move rather than silently omitting them.
type Evacuation struct {
	Requested int                 `json:"requested"`
	Skipped   int                 `json:"skipped,omitempty"` // already mid-migration
	Failures  []EvacuationFailure `json:"failures"`
}

type EvacuationFailure struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Error     string `json:"error"`
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
	// Minimums from spec.requirements: sizing below them fails KubeVirt's
	// webhook at sync time, so forms refuse it at stage time.
	MinCPU    int64  `json:"minCPU,omitempty"`
	MinMemory string `json:"minMemory,omitempty"`
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

//
// dotvirt presents OVN-K networking in VMware terms: a Network is a port group a
// VM NIC attaches to; an Uplink is the physical-adapter binding (the vDS uplink);
// a PhysicalAdapter is one node NIC. The OVN-K objects behind them (UDN, CUDN,
// localnet, NAD) and nmstate (NNCP, NNS) never surface to the user.
