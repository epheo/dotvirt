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
	Topology  string `json:"topology,omitempty"` // the segment's topology label values; empty = cluster-wide
	Free      int64  `json:"free"`
	MaxVolume int64  `json:"maxVolume,omitempty"` // largest single volume the segment can provision
}
