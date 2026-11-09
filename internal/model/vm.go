package model

import "strings"

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

	// Migration is the live (or last) node-to-node move and its
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
	Type         string `json:"type,omitempty"`         // dataVolume | pvc | emptyDisk | containerDisk | cloudInitNoCloud | ...
	Size         string `json:"size,omitempty"`         // emptyDisk capacity or dataVolume requested storage
	StorageClass string `json:"storageClass,omitempty"` // dataVolume storageClassName (empty = cluster default)
	// Claim is the PersistentVolumeClaim behind a dataVolume or pvc disk (a
	// DataVolume names its claim after itself).
	Claim string `json:"claim,omitempty"`
	// AccessModes are the DataVolume's explicit access modes; empty means CDI
	// applies the class profile's first claim property set.
	AccessModes []string `json:"accessModes,omitempty"`
	// Image is the boot image ("namespace/name" of a DataSource) the disk was
	// provisioned from; empty for blank, imported or cloned disks.
	Image string `json:"image,omitempty"`
	// Import is where CDI downloaded the disk from; nil unless its
	// DataVolume template imports from a registry or a URL.
	Import *DiskImport `json:"import,omitempty"`
}

// The DataVolume sources an import downloads from.
const (
	ImportRegistry = "registry"
	ImportURL      = "url"
)

// DiskImport is an import source as it may be shown: never the query string
// or credentials a URL can carry (a pre-signed URL's signature lives in its
// query), and a registry reference without its docker:// scheme.
type DiskImport struct {
	Kind   string `json:"kind"`   // ImportRegistry | ImportURL
	Source string `json:"source"` // scheme, host and path of a URL; a registry's image reference
	// Redacted: the source carried a query string or credentials left out
	// here, so it cannot be fetched again as shown.
	Redacted bool `json:"redacted,omitempty"`
}

// ImportFrom is how the url of a DataVolume's registry or http source is
// shown. It is the one place that strips it: a source reaches the wire, an
// annotation or a log only through here.
func ImportFrom(kind, url string) *DiskImport {
	imp := &DiskImport{Kind: kind}
	s := strings.TrimSpace(url)
	// A query precedes any fragment; the fragment never reaches the server.
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		imp.Redacted = s[i] == '?'
		s = s[:i]
	}
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok {
		scheme, rest = "", s
	}
	host, path, hasPath := strings.Cut(rest, "/")
	// A registry reference without a path may end in @digest, never userinfo.
	if at := strings.LastIndex(host, "@"); at >= 0 && (kind != ImportRegistry || hasPath) {
		host = host[at+1:]
		imp.Redacted = true
	}
	if hasPath {
		host += "/" + path
	}
	switch {
	case scheme == "", kind == ImportRegistry && strings.EqualFold(scheme, "docker"):
		imp.Source = host
	default:
		imp.Source = strings.ToLower(scheme) + "://" + host
	}
	return imp
}

// PodNetwork is the Network of a NIC on the pod network, where any other value
// is a multus networkName.
const PodNetwork = "pod"

// NIC is a network interface on the VM. Name/Network come from the manifest;
// MAC/IP are merged from the live VMI status when running.
type NIC struct {
	Name    string `json:"name"`
	Network string `json:"network,omitempty"` // PodNetwork or the multus networkName
	MAC     string `json:"mac,omitempty"`     // live, from VMI status
	IP      string `json:"ip,omitempty"`      // live, from VMI status
	// Implicit marks the pod-network adapter KubeVirt attaches when the manifest
	// declares none: real on the VMI, absent from the file, so not removable.
	Implicit bool `json:"implicit,omitempty"`
}

// InterfaceName is the name a VM interface takes from the network it attaches:
// the NetworkAttachmentDefinition's own name, without the namespace a
// <namespace>/<name> reference carries.
func InterfaceName(networkRef string) string {
	if i := strings.LastIndex(networkRef, "/"); i >= 0 {
		return networkRef[i+1:]
	}
	return networkRef
}

// ProjectNamespace is one namespace bucket within a project: the VMs it holds.
type ProjectNamespace struct {
	Namespace string `json:"namespace"`
	VMs       []VM   `json:"vms"`
	// CanAuthor is the caller's update right on VMs here, or on NetworkPolicies
	// on a cluster with no VMs: the UI hides what would 403.
	// Nil when the review failed this frame: unknown, and the UI fails closed,
	// offering nothing and saying the access could not be checked.
	CanAuthor *bool `json:"canAuthor,omitempty"`
}

// Project is a tenant in the inventory tree: a named set of
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
	// computes across every object the repo declares, so it reflects networks, network
	// policies and tenancy, not just VMs. Nil when Argo isn't wired or no Application
	// tracks this repo.
	GitOps *ProjectSync `json:"gitOps,omitempty"`
	// Unnamespaced are the objects the repo declares without metadata.namespace,
	// under the namespace dotvirt reads from their directory. Argo applies them
	// to the Application's destination namespace instead, so each is an issue
	// to fix in git.
	Unnamespaced []ObjectRef `json:"unnamespaced,omitempty"`
	// OutOfProject are the objects the repo declares in a namespace outside the
	// project. Its AppProject refuses them, which fails the Application's whole
	// sync, so each is an issue to fix in git.
	OutOfProject []ObjectRef `json:"outOfProject,omitempty"`
	// PlatformKinds are the objects the repo declares of a kind only the
	// platform repo may hold, a NetworkAttachmentDefinition say. Its AppProject
	// refuses them, which fails the Application's whole sync, so each is an
	// issue to fix in git.
	PlatformKinds []ObjectRef `json:"platformKinds,omitempty"`
}

// AdoptableNamespace is a namespace holding VMs but no dotvirt.io/project label -
// an existing tenant on a cluster dotvirt landed on. Adopting it is the create-
// project flow with its name: the staged Namespace manifest labels it on merge.
type AdoptableNamespace struct {
	Namespace string `json:"namespace"`
	VMs       int    `json:"vms"`
}

// ProjectSync is a project's overall GitOps state, read straight from its managing
// ArgoCD Application. Operation is the last sync's phase ("Running" = applying,
// "Failed"/"Error" = apply failed), the cue for a "pending apply" or an alarm that a
// per-VM view can't give - a merged network or policy PR that fails to apply shows
// here even though no VM moved.
type ProjectSync struct {
	Sync      SyncStatus `json:"sync,omitempty"`
	Health    string     `json:"health,omitempty"`    // Healthy | Degraded | Progressing | Missing | ...
	Operation string     `json:"operation,omitempty"` // operationState.phase: Running | Succeeded | Failed | Error
	SyncError string     `json:"syncError,omitempty"` // operationState.message when the last sync didn't succeed
	Revision  string     `json:"revision,omitempty"`  // short applied git revision
	// Unhealthy names what the Application holds that is degraded or was refused,
	// with Argo's own reason: the rollup alone says only "Degraded".
	Unhealthy []ObjectHealth `json:"unhealthy,omitempty"`
}

// ObjectHealth is one object an Application reports as degraded or refused: its
// identity, the health Argo computed, and the health or apply message.
type ObjectHealth struct {
	Group     string `json:"group,omitempty"` // the API group; empty for the core group
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Health    string `json:"health,omitempty"` // Degraded | Missing | Unknown; empty for a refused apply
	Message   string `json:"message,omitempty"`
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
	// NetworksVersion fingerprints, for this caller only, what its out-of-band
	// network and policy pulls (GET /api/networks, /api/policies) read: the
	// catalog objects and their drift in its visible namespaces, the
	// cluster-scoped ones, and the visible set itself. The frontend re-pulls
	// when it changes, so a merged network PR and its sync badge appear live,
	// and another tenant's change never moves it. Equality-only: not monotonic,
	// and a server restart changes it. Fits a JavaScript number.
	NetworksVersion uint64 `json:"networksVersion,omitempty"`
	// TasksVersion is the same contract for the recent-tasks feed (GET
	// /api/tasks): the entries this caller's pull returns.
	TasksVersion uint64 `json:"tasksVersion,omitempty"`
	// ReplicationVersion is the same contract for GET /api/storage/replication
	// and /api/storage/protection. Absent while the cluster serves no r-drbd.
	ReplicationVersion uint64 `json:"replicationVersion,omitempty"`
	// RoutesVersion is the same contract for GET /api/networking/gateways and
	// /api/networking/routes. Absent while no GatewayClass exists.
	RoutesVersion uint64 `json:"routesVersion,omitempty"`
	// Adoptable proposes existing tenants: namespaces with VMs but no project
	// label. Present only for callers with namespace-create authority (the
	// adoption gate) - the candidate scan is cluster-wide, so the SSAR is what
	// keeps one tenant from enumerating another's namespaces.
	Adoptable []AdoptableNamespace `json:"adoptable,omitempty"`
	// Platform is the platform Application's rollup. The tier has no project row,
	// so what it failed to apply or keeps unhealthy rides here for the issues
	// plane. Nil when no platform repo is configured or Argo is off / pre-sync.
	Platform *ProjectSync `json:"platform,omitempty"`
}
