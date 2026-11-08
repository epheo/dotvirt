package model

// Capabilities is GET /api/capabilities: the caller's verdict on each action
// dotvirt gates, each asking every question its route, the coordinator's gate
// on what it stages, or the apiserver on an imperative call asks of it,
// weighed as the gate weighs them, so the UI offers only what the server
// allows. A review that failed reads as false:
// unknown never offers an action. Without ?namespace= it answers Cluster; with
// it, Namespace.
type Capabilities struct {
	Cluster   *ClusterCaps   `json:"cluster,omitempty"`
	Namespace *NamespaceCaps `json:"namespace,omitempty"`
}

// ObjectCaps are creating, updating and deleting one kind through git: the
// create form, the edit form and manifest editor (and restore), and delete.
type ObjectCaps struct {
	Create bool `json:"create"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

// ClusterCaps are the platform tier's actions and the cluster-scoped ones.
type ClusterCaps struct {
	// Platform is any platform-tier field below (OAuthClient aside): the
	// caller may stage a platform change, and so reach their own platform
	// draft once they hold one.
	Platform bool `json:"platform"`
	// PlatformRead opens the platform tier's shared reads: its history, its
	// open proposals and their diffs, revert, and its merges and rollup
	// detail in the frame. A cluster-wide signal: the platform repo names
	// every project, namespace and owner. It needs list on every
	// cluster-scoped kind dotvirt manages too, as the repo declares those
	// objects.
	PlatformRead bool `json:"platformRead"`
	// Network is the shared networks (ClusterUserDefinedNetwork).
	Network                    ObjectCaps `json:"network"`
	Uplink                     ObjectCaps `json:"uplink"`
	EgressIP                   ObjectCaps `json:"egressIP"`
	ExternalRoute              ObjectCaps `json:"externalRoute"`
	AdminNetworkPolicy         ObjectCaps `json:"adminNetworkPolicy"`
	BaselineAdminNetworkPolicy ObjectCaps `json:"baselineAdminNetworkPolicy"`
	// NamespaceCreate is a new namespace, for New Namespace and New Project;
	// VMNetwork gives it a primary VM Network, GrantAdmin names its owners.
	NamespaceCreate bool `json:"namespaceCreate"`
	VMNetwork       bool `json:"vmNetwork"`
	GrantAdmin      bool `json:"grantAdmin"`
	// SharedLibrary writes the shared template library.
	SharedLibrary bool `json:"sharedLibrary"`
	// DRSEnable stages the rebalancing file set, DRSPSI with the PSI MachineConfig;
	// DRSDisable removes the KubeDescheduler.
	DRSEnable  bool `json:"drsEnable"`
	DRSPSI     bool `json:"drsPSI"`
	DRSDisable bool `json:"drsDisable"`
	// OAuthClient registers the SSO OAuthClient (finish SSO).
	OAuthClient bool `json:"oauthClient"`
	// ImageUpload is this dotvirt knowing an upload proxy: without one every
	// upload is refused whatever the caller's rights, and the namespace
	// fields DataVolumeCreate and BootImageCreate read false.
	ImageUpload bool `json:"imageUpload"`
}

// NamespaceCaps are the actions in one namespace and on its project.
type NamespaceCaps struct {
	Namespace string `json:"namespace"`
	// VM is the VirtualMachines staged through git: create (a new VM, a
	// template deploy), update (edit, adopt, restore), delete.
	VM ObjectCaps `json:"vm"`
	// Resync may resync running VMs here (their update); false without
	// ArgoCD. A list view's signal: the one VM's verdict, an absent or a
	// replaced one included, is DriftResult.CanResync.
	Resync bool `json:"resync"`
	// The imperative operations, run under the caller's token.
	Restart          bool `json:"restart"`
	Migrate          bool `json:"migrate"`
	Pause            bool `json:"pause"`
	Unpause          bool `json:"unpause"`
	Console          bool `json:"console"`
	SnapshotTake     bool `json:"snapshotTake"`
	SnapshotRestore  bool `json:"snapshotRestore"`
	SnapshotDelete   bool `json:"snapshotDelete"`
	Clone            bool `json:"clone"`
	DataVolumeCreate bool `json:"dataVolumeCreate"` // image upload
	// BootImageCreate is an upload registered as a boot image (Add a boot
	// image): the upload plus a DataSource over it.
	BootImageCreate bool `json:"bootImageCreate"`
	// BootImageImport is a boot image the cluster downloads from a registry
	// or a URL: its DataVolume and DataSource. It needs no upload proxy.
	BootImageImport bool `json:"bootImageImport"`
	// The namespaced network family, staged through git. Network is the
	// project networks (UserDefinedNetwork).
	Network ObjectCaps `json:"network"`
	// NetworkAttachment is a NetworkAttachmentDefinition git still declares
	// under the network routes (update, delete): no form creates one.
	NetworkAttachment ObjectCaps  `json:"networkAttachment"`
	NetworkPolicy     ObjectCaps  `json:"networkPolicy"`
	EgressFirewall    ObjectCaps  `json:"egressFirewall"`
	Project           ProjectCaps `json:"project"`
}

// ProjectCaps are the actions on a namespace's whole project, over every one
// of its namespaces, those hidden from the caller included.
type ProjectCaps struct {
	Name string `json:"name"`
	// TemplateEdit writes the project's template library.
	TemplateEdit bool `json:"templateEdit"`
	// NewNamespace joins a new namespace to the project (with a VM Network,
	// ClusterCaps.VMNetwork too).
	NewNamespace bool `json:"newNamespace"`
	// Adopt attaches a repo to the project: a repoless one's, or Recover repo
	// on one whose forge lost or re-homed it (the route refuses a healthy
	// repo). GrantAdmin names owners while doing so. Release dissolves a
	// repoless project, false for one with a repo.
	Adopt      bool `json:"adopt"`
	GrantAdmin bool `json:"grantAdmin"`
	Release    bool `json:"release"`
	// SyncAccess grants the project's Argo sync account in its namespaces'
	// RoleBindings; false once it holds them, or with no sync namespace.
	SyncAccess bool `json:"syncAccess"`
}
