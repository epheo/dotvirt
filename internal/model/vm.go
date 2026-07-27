package model

// The VM inventory: what a project runs and how each VM presents in the tree.

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
	// Scheduling is the VM's placement policy (placement groups + host pinning);
	// nil when the manifest carries none.
	Scheduling *VMScheduling `json:"scheduling,omitempty"`

	// From cluster (actual state), when cluster reads are enabled.
	Phase        string   `json:"phase,omitempty"`  // VMI phase, e.g. Running
	Paused       bool     `json:"paused,omitempty"` // VMI Paused condition (phase stays Running)
	GuestIP      string   `json:"guestIP,omitempty"`
	IPs          []string `json:"ips,omitempty"` // every guest-reported IP
	NodeName     string   `json:"nodeName,omitempty"`
	OS           string   `json:"os,omitempty"`           // guest-agent OS pretty name
	MemoryActual string   `json:"memoryActual,omitempty"` // current guest memory (hotplug-aware)
	VCPUs        int      `json:"vcpus,omitempty"`        // rendered vCPU topology; the summary's value when the manifest delegates sizing to an instancetype
	StartedAt    string   `json:"startedAt,omitempty"`    // RFC3339; VMI entered Running (for uptime)

	// Migration is the live (or last) node-to-node move - vCenter's vMotion
	// progress, read from the VMI's migration state. Nil when never migrated.
	Migration *Migration `json:"migration,omitempty"`

	// From ArgoCD, when enabled.
	Sync   SyncStatus `json:"sync"`
	Health string     `json:"health,omitempty"`
	// SyncError is ArgoCD's apply failure for this VM (e.g. a webhook rejection),
	// surfaced so the UI can explain an OutOfSync VM instead of just flagging it.
	SyncError string `json:"syncError,omitempty"`
}

// PlacementGroup is one named scheduling rule: VMs sharing the group are kept
// on one host ("together") or spread across hosts ("apart"). Strict renders a
// required scheduling term (the scheduler refuses violating placements);
// otherwise the term is preferred (best effort).
type PlacementGroup struct {
	Name   string `json:"name"`
	Mode   string `json:"mode"` // together | apart
	Strict bool   `json:"strict,omitempty"`
}

// VMScheduling is a VM's placement policy as read from its manifest. Custom
// flags affinity/node-selection content dotvirt does not own - such VMs are
// edited in git, never through the scheduling form.
type VMScheduling struct {
	Pin    []string         `json:"pin,omitempty"` // host names the VM must run on
	Groups []PlacementGroup `json:"groups,omitempty"`
	Custom bool             `json:"custom,omitempty"`
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
	Type         string `json:"type,omitempty"`         // dataVolume | emptyDisk | containerDisk | cloudInitNoCloud | ...
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
// left empty) when a project's namespaces are labeled but have no usable repo -
// surfaced as a warning in the UI rather than failing the whole inventory.
type Project struct {
	Name       string             `json:"name"`
	Repo       string             `json:"repo,omitempty"`
	Namespaces []ProjectNamespace `json:"namespaces"`
	Error      string             `json:"error,omitempty"`
	// GitOps is the project's ArgoCD Application rollup - the sync/health Argo already
	// computes across every object the repo declares, so it reflects segments, network
	// policies and tenancy, not just VMs. Nil when Argo isn't wired or no Application
	// tracks this repo.
	GitOps *ProjectSync `json:"gitOps,omitempty"`
}

// ProjectSync is a project's overall GitOps state, read straight from its managing
// ArgoCD Application. Operation is the last sync's phase ("Running" = applying,
// "Failed"/"Error" = apply failed), the cue for a "pending apply" or an alarm that a
// per-VM view can't give - a merged segment or policy PR that fails to apply shows
// here even though no VM moved.
type ProjectSync struct {
	Sync      SyncStatus `json:"sync,omitempty"`
	Health    string     `json:"health,omitempty"`    // Healthy | Degraded | Progressing | Missing | ...
	Operation string     `json:"operation,omitempty"` // operationState.phase: Running | Succeeded | Failed | Error
	SyncError string     `json:"syncError,omitempty"` // operationState.message when the last sync didn't succeed
	Revision  string     `json:"revision,omitempty"`  // short applied git revision
}

// Inventory is the full multi-project tree. Warnings carry non-fatal degradations
// (e.g. live or drift state couldn't be read) so the UI can say "status
// unavailable" instead of silently rendering every VM as stopped / not-tracked.
// Proposals rides along so the open-PR lane updates over the live stream - a PR
// merged anywhere (the git poll sees main move) repaints it with no client poll.
type Inventory struct {
	Projects  []Project  `json:"projects"`
	Warnings  []string   `json:"warnings,omitempty"`
	Proposals []Proposal `json:"proposals,omitempty"`
	// NetworksVersion is a monotonic watermark that moves when a port group changes
	// (NetworkChanged) or a non-VM object's drift content changes (the argo snapshot's
	// ObjectDriftGen). The network catalog is fetched out-of-band (GET /api/networks,
	// not on this frame), so the frontend re-pulls it when this bumps: a merged segment
	// PR - and its sync badge - then appear live instead of only on reload.
	NetworksVersion uint64 `json:"networksVersion,omitempty"`
	// TasksVersion is the same contract for the recent-tasks feed (GET /api/tasks,
	// fetched out-of-band): bumps when an op is recorded or a merged PR lands.
	TasksVersion uint64 `json:"tasksVersion,omitempty"`
}
