package model

// StorageClassInfo is the Storage section's fact sheet for one class: the
// StorageClass object, what CDI's StorageProfile gives a DataVolume on it,
// and the free capacity the CSI driver publishes. Every source is
// cluster-scoped platform truth, so one SA read serves every caller.
type StorageClassInfo struct {
	Name          string `json:"name"`
	Default       bool   `json:"default,omitempty"`     // the cluster's default class annotation
	Description   string `json:"description,omitempty"` // the platform's description annotation
	Provisioner   string `json:"provisioner"`
	ReclaimPolicy string `json:"reclaimPolicy,omitempty"`
	BindingMode   string `json:"bindingMode,omitempty"` // Immediate | WaitForFirstConsumer
	Expandable    bool   `json:"expandable,omitempty"`  // allowVolumeExpansion: disk resize possible
	Created       string `json:"created,omitempty"`     // RFC3339
	// SnapshotClass is the VolumeSnapshotClass VM snapshots on this class use:
	// CDI's pick when its profile names one, else the first class registered
	// for the provisioner. Empty means VM snapshots cannot be taken here.
	SnapshotClass string `json:"snapshotClass,omitempty"`

	// Profile is CDI's StorageProfile for the class; nil when CDI has none,
	// which means DataVolumes there need explicit claim settings.
	Profile *StorageProfile `json:"profile,omitempty"`

	// Free is the unallocated capacity the CSI driver reports, summed over the
	// class's topology segments. Nil when the driver publishes no
	// CSIStorageCapacity (most network storage doesn't), so the UI shows
	// nothing rather than zero.
	Free     *int64            `json:"free,omitempty"`
	Segments []CapacitySegment `json:"segments,omitempty"`
}

// StorageProfile is what a DataVolume gets on the class when its template
// leaves claim settings blank (CDI fills them from here).
type StorageProfile struct {
	AccessModes []string `json:"accessModes"` // the preferred claim property set
	VolumeMode  string   `json:"volumeMode,omitempty"`
	// Shared is true when any property set offers ReadWriteMany: the access
	// mode KubeVirt needs to live-migrate a VM with a disk on this class.
	Shared        bool   `json:"shared,omitempty"`
	CloneStrategy string `json:"cloneStrategy,omitempty"` // snapshot | copy | csi-clone
	Recognized    bool   `json:"recognized"`              // CDI knows the provisioner's capabilities
}

// CapacitySegment is one CSIStorageCapacity entry: free capacity in one
// topology segment (a node for local storage, or the whole cluster).
type CapacitySegment struct {
	Topology string `json:"topology,omitempty"` // the segment's topology label values; empty = cluster-wide
	Free     int64  `json:"free"`
}

// Replication is GET /api/storage/replication: where every copy of every
// replicated disk lives, and the room left. Operator-only: it names hosts.
type Replication struct {
	// Synced is false until every watch's first list landed, while an empty
	// list means unknown rather than none.
	Synced bool `json:"synced"`
	// Stale: a watch is down and this is its last known state.
	Stale   bool               `json:"stale,omitempty"`
	Pools   []ReplicationPool  `json:"pools"`
	Volumes []ReplicatedVolume `json:"volumes"`
	Hosts   []ReplicationHost  `json:"hosts"`
}

// ReplicationPool is one r-drbd Pool: a tier's room on one host.
type ReplicationPool struct {
	Name       string `json:"name"`
	Node       string `json:"node"`
	Tier       string `json:"tier"`
	TotalBytes int64  `json:"totalBytes"`
	FreeBytes  int64  `json:"freeBytes"`
	// PlaceableBytes is the largest new copy placement accepts here now.
	PlaceableBytes int64  `json:"placeableBytes"`
	Available      bool   `json:"available"`
	Reason         string `json:"reason,omitempty"` // the Available condition's reason
}

// ReplicationHost is one host r-drbd's agent reports on.
type ReplicationHost struct {
	Name  string      `json:"name"`
	Blank []BlankDisk `json:"blank,omitempty"` // disks a Pool could take
}

// BlankDisk is an unclaimed whole disk.
type BlankDisk struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	Model     string `json:"model,omitempty"`
}

// ReplicatedVolume is one r-drbd Volume and the claim it serves.
type ReplicatedVolume struct {
	Name        string   `json:"name"`
	Namespace   string   `json:"namespace,omitempty"`
	Claim       string   `json:"claim,omitempty"`
	SizeBytes   int64    `json:"sizeBytes"`
	Copies      int      `json:"copies"` // the copies asked for
	Tier        string   `json:"tier"`
	Phase       string   `json:"phase,omitempty"`
	Primary     string   `json:"primary,omitempty"` // the host the device is open on
	PinnedNode  string   `json:"pinnedNode,omitempty"`
	PinnedPool  string   `json:"pinnedPool,omitempty"`
	Attachments []string `json:"attachments,omitempty"` // hosts the device is published to
	SplitBrain  bool     `json:"splitBrain,omitempty"`
	// SplitSince is when this split began. A resolve sends it back, so a dialog
	// left open cannot pick the winner of a later split.
	SplitSince string          `json:"splitSince,omitempty"`
	Resolving  string          `json:"resolving,omitempty"` // the survivor already named, while r-drbd works
	QuorumLost bool            `json:"quorumLost,omitempty"`
	Evicting   []string        `json:"evicting,omitempty"` // hosts whose copy is being moved off
	Replicas   []VolumeReplica `json:"replicas"`
}

// What a replica is doing, read from every replica's report.
const (
	ReplicaInSync      = "in-sync"
	ReplicaCatchingUp  = "catching-up"
	ReplicaBehind      = "behind"
	ReplicaStandby     = "standby" // a reachable witness or diskless attachment
	ReplicaUnreachable = "unreachable"
	ReplicaUnknown     = "unknown" // assigned, not reported yet
)

// VolumeReplica is one assigned replica.
type VolumeReplica struct {
	Node        string `json:"node"`
	Pool        string `json:"pool,omitempty"`
	Role        string `json:"role"` // diskful, diskless or tiebreaker
	State       string `json:"state"`
	DiskState   string `json:"diskState,omitempty"`
	SyncPercent *int   `json:"syncPercent,omitempty"`
	InUse       bool   `json:"inUse,omitempty"`
}

// What a disk's owner can act on.
const (
	StandingProtected     = "protected"
	StandingLocal         = "local" // one copy by choice
	StandingCatchingUp    = "catching-up"
	StandingExposed       = "exposed" // fewer copies in sync than asked, none catching up
	StandingPending       = "pending"
	StandingNeedsOperator = "needs-operator"
)

// ReplicationEvict is what moving the copies off a host does, or would do on a
// dry run: the replicas leaving, and what stays and why.
type ReplicationEvict struct {
	Moving  []string          `json:"moving"`
	Skipped []ReplicationSkip `json:"skipped,omitempty"`
}

// ReplicationSkip is one volume that keeps its replica on the host.
type ReplicationSkip struct {
	Volume string `json:"volume"`
	Reason string `json:"reason"`
}

// DiskProtection is GET /api/storage/protection's row: a replicated disk's
// standing as its owner may read it. Never a host or a pool.
type DiskProtection struct {
	Namespace   string `json:"namespace"`
	Claim       string `json:"claim"`
	Copies      int    `json:"copies"`
	InSync      int    `json:"inSync"`
	Standing    string `json:"standing"`
	SyncPercent *int   `json:"syncPercent,omitempty"`
}
