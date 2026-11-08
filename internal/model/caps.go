package model

// Capabilities is GET /api/capabilities: the caller's verdict on each action
// dotvirt gates, each asking every question its route, the coordinator's gate
// on what it stages, or the apiserver on an imperative call asks of it, so the
// UI offers only what the server allows. A review that failed reads as false:
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
	// Platform is the platform draft lane: view, propose and revert the
	// caller's own platform draft, and adopt cluster-scoped objects (each
	// captured kind then needs its own create right).
	Platform bool `json:"platform"`
	// Network is the shared segments (ClusterUserDefinedNetwork).
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
	// DRSEnable stages the DRS file set, DRSPSI with the PSI MachineConfig;
	// DRSDisable removes the KubeDescheduler.
	DRSEnable  bool `json:"drsEnable"`
	DRSPSI     bool `json:"drsPSI"`
	DRSDisable bool `json:"drsDisable"`
	// OAuthClient registers the SSO OAuthClient (finish SSO).
	OAuthClient bool `json:"oauthClient"`
}

// NamespaceCaps are the actions in one namespace and on its project.
type NamespaceCaps struct {
	Namespace string `json:"namespace"`
	// VM is the VirtualMachines staged through git: create (a new VM, a
	// template deploy), update (edit, adopt, restore), delete.
	VM ObjectCaps `json:"vm"`
	// Resync drives an ArgoCD sync of one VM; false without ArgoCD.
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
	// The namespaced network family, staged through git. Network is the
	// project segments (UserDefinedNetwork).
	Network        ObjectCaps  `json:"network"`
	NetworkPolicy  ObjectCaps  `json:"networkPolicy"`
	EgressFirewall ObjectCaps  `json:"egressFirewall"`
	Project        ProjectCaps `json:"project"`
}

// ProjectCaps are the actions on a namespace's whole project, over every one
// of its namespaces, those hidden from the caller included.
type ProjectCaps struct {
	Name string `json:"name"`
	// TemplateEdit writes the project's template library.
	TemplateEdit bool `json:"templateEdit"`
	// Adopt attaches a repo to the repoless project; GrantAdmin names owners
	// while doing so. Release dissolves it.
	Adopt      bool `json:"adopt"`
	GrantAdmin bool `json:"grantAdmin"`
	Release    bool `json:"release"`
}
