package model

// DTOs crossing the API boundary: the draft/changeset request and view types.

// Change is one human-readable, YAML-free change item (a semantic diff entry).
// Action is "change" (From->To), "add" (To), or "remove" (From).
type Change struct {
	Field  string `json:"field"`
	Action string `json:"action"` // change | add | remove
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// VMEdit is a set of field changes to apply to a VirtualMachine manifest. Nil/
// empty fields are left untouched, so the UI can change just one thing. One
// definition serves the API request body, the persisted draft store, and the
// manifest editor.
type VMEdit struct {
	Power        *string `json:"power,omitempty"` // "On" | "Off" -> runStrategy Always/Halted (or legacy running bool)
	CPUCores     *int    `json:"cpuCores,omitempty"`
	Memory       *string `json:"memory,omitempty"`       // e.g. "4Gi"
	Instancetype *string `json:"instancetype,omitempty"` // spec.instancetype.name
	Preference   *string `json:"preference,omitempty"`   // spec.preference.name

	// Sizing selects which representation owns CPU/memory: "instancetype" (an
	// instancetype reference; inline domain.cpu/memory must be absent) or "custom"
	// (inline domain.cpu/memory; no instancetype). KubeVirt rejects a VM that has
	// both, so the two are mutually exclusive. Nil leaves the representation as-is.
	Sizing *string `json:"sizing,omitempty"`

	// Label edits: keys to set (upsert) and keys to remove.
	SetLabels    map[string]string `json:"setLabels,omitempty"`
	RemoveLabels []string          `json:"removeLabels,omitempty"`

	// DRSExclude toggles the descheduler's prefer-no-eviction annotation on the
	// VM template: true keeps the VM live-migratable for maintenance while the
	// automatic load balancer (DRS) leaves it alone; false removes the
	// annotation. Nil leaves it untouched.
	DRSExclude *bool `json:"drsExclude,omitempty"`

	// EvictionStrategy sets spec.template.spec.evictionStrategy (LiveMigrate,
	// None, ...); the empty string removes it, falling back to the cluster
	// default. Nil leaves it untouched.
	EvictionStrategy *string `json:"evictionStrategy,omitempty"`

	// Disk/network edits on the VM template.
	AddDisks       []DiskAdd    `json:"addDisks,omitempty"`
	RemoveDisks    []string     `json:"removeDisks,omitempty"` // disk names to remove
	AddNetworks    []NetworkAdd `json:"addNetworks,omitempty"`
	RemoveNetworks []string     `json:"removeNetworks,omitempty"` // network/interface names to remove

	// MigrateVolumes moves disks to other storage classes (storage live
	// migration): each entry replaces the disk's DataVolume template with a
	// blank one on the target class, and the edit sets
	// spec.updateVolumesStrategy: Migration so KubeVirt live-copies the data
	// on merge. Reverting the commit is the migration cancel.
	MigrateVolumes []VolumeMigration `json:"migrateVolumes,omitempty"`

	// Pin replaces the VM's host pinning with a required node-affinity In-list
	// on kubernetes.io/hostname; an empty list removes it. Nil leaves it alone.
	Pin *[]string `json:"pin,omitempty"`
	// AddGroups/RemoveGroups edit named placement groups: a membership label
	// on the template plus a pod (anti-)affinity term against that label
	// (manifest/scheduling.go holds the encoding). AddGroups upserts, so
	// re-adding a group changes its mode/strictness.
	AddGroups    []PlacementGroup `json:"addGroups,omitempty"`
	RemoveGroups []string         `json:"removeGroups,omitempty"`
}

// Empty reports whether the edit changes nothing.
func (e VMEdit) Empty() bool {
	return e.Power == nil && e.CPUCores == nil && e.Memory == nil &&
		e.Instancetype == nil && e.Preference == nil && e.Sizing == nil &&
		len(e.SetLabels) == 0 && len(e.RemoveLabels) == 0 &&
		e.DRSExclude == nil && e.EvictionStrategy == nil &&
		len(e.AddDisks) == 0 && len(e.RemoveDisks) == 0 &&
		len(e.AddNetworks) == 0 && len(e.RemoveNetworks) == 0 &&
		len(e.MigrateVolumes) == 0 &&
		e.Pin == nil && len(e.AddGroups) == 0 && len(e.RemoveGroups) == 0
}

// EditRequest is the body of an edit: which VM source file, and which fields to
// change. Embedding flattens VMEdit's fields into the request body.
type EditRequest struct {
	SourceFile string `json:"sourceFile"`
	VMEdit
}

// DiskAdd / NetworkAdd are the add-device entries in an EditRequest body.
type DiskAdd struct {
	Name         string `json:"name"`
	Size         string `json:"size"`
	StorageClass string `json:"storageClass,omitempty"` // blank DataVolume class; empty = cluster default
}

type NetworkAdd struct {
	Name string `json:"name"`
}

// VolumeMigration moves one disk to another storage class (storage live
// migration): Name is the volume name on the VM template, StorageClass the
// target class its replacement DataVolume is provisioned on.
type VolumeMigration struct {
	Name         string `json:"name"`
	StorageClass string `json:"storageClass"`
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

// ObjectRef identifies one declared object, kind-agnostic. Namespace is empty for
// cluster-scoped kinds. The identity git manifests, Argo resource status, and the
// draft all share, so coverage can be compared across the three.
type ObjectRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// DraftItem is one pending change rendered for the UI.
type DraftItem struct {
	Kind      string   `json:"kind"`               // edit | create | delete
	Resource  string   `json:"resource,omitempty"` // "" == vm | network - disambiguates unstage
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Changes   []Change `json:"changes"`
	YAML      string   `json:"yaml,omitempty"` // raw/edited manifest for the collapsed view
}

// DraftView is the whole draft changeset as semantic items. Warning is DERIVED
// each render (prune risk plus capture caveats), never stored: a stored warning
// outlives the state it describes.
type DraftView struct {
	Base    string      `json:"base"`
	Branch  string      `json:"branch"`
	Count   int         `json:"count"`
	Items   []DraftItem `json:"items"`
	Warning string      `json:"warning,omitempty"`
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

// Proposal is an open pull request backing a project's draft - the staged->PR->
// synced lifecycle's middle state, surfaced as a Recent Tasks row.
type Proposal struct {
	Project  string `json:"project"`
	PRNumber int    `json:"prNumber"`
	PRURL    string `json:"prURL"`
	Title    string `json:"title,omitempty"`
}

// TaskEntry is one Recent Tasks row: an imperative runtime op dotvirt performed
// as the caller ("op"), or a PR merged into a project's base branch ("merge").
// Server-derived so every browser sees every admin's acts with real attribution;
// ops live in memory only (the durable audit trail is the cluster's audit log -
// ops run under the caller's own token), merges re-derive from the forge.
type TaskEntry struct {
	Kind      string `json:"kind"` // "op" | "merge"
	Verb      string `json:"verb"`
	Namespace string `json:"namespace,omitempty"` // empty for node-scoped ops
	Name      string `json:"name,omitempty"`      // VM or node name
	Project   string `json:"project,omitempty"`
	PRNumber  int    `json:"prNumber,omitempty"`
	PRURL     string `json:"prURL,omitempty"`
	Title     string `json:"title,omitempty"`
	By        string `json:"by,omitempty"`
	OK        bool   `json:"ok"`
	At        string `json:"at"` // RFC3339
}

// Permissions is the caller's effective capability set in one namespace - the
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
