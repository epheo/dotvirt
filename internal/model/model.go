// Package model holds the API-facing types shared across dotvirt's planes.
package model

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

	// From cluster (actual state), when cluster reads are enabled.
	Phase    string `json:"phase,omitempty"` // VMI phase, e.g. Running
	GuestIP  string `json:"guestIP,omitempty"`
	NodeName string `json:"nodeName,omitempty"`

	// From ArgoCD, when enabled.
	Sync   SyncStatus `json:"sync"`
	Health string     `json:"health,omitempty"`
}

// Disk is a disk device on the VM (from the template).
type Disk struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"` // dataVolume | emptyDisk | containerDisk | cloudInitNoCloud | …
	Size string `json:"size,omitempty"` // for emptyDisk capacity, when known
}

// NIC is a network interface on the VM.
type NIC struct {
	Name    string `json:"name"`
	Network string `json:"network,omitempty"` // "pod" or the multus networkName
}

// Project is a namespace bucket in the vCenter-style inventory tree.
type Project struct {
	Namespace string `json:"namespace"`
	VMs       []VM   `json:"vms"`
}

// Inventory is the full tree for one branch.
type Inventory struct {
	Branch   string    `json:"branch"`
	Projects []Project `json:"projects"`
}

// Change is one human-readable, YAML-free change item (a semantic diff entry).
// Action is "change" (From→To), "add" (To), or "remove" (From).
type Change struct {
	Field  string `json:"field"`
	Action string `json:"action"` // change | add | remove
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// --- DTOs returned across the API boundary ---

// DriftResult is a VM's drift (running vs main) as semantic changes.
type DriftResult struct {
	Drift   bool     `json:"drift"`
	Changes []Change `json:"changes"`
}

// DraftItem is one VM's pending change rendered for the UI.
type DraftItem struct {
	Kind      string   `json:"kind"` // edit | create
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

// ResyncResult reports which ArgoCD Application was synced.
type ResyncResult struct {
	Application string `json:"application"`
	Revision    string `json:"revision"`
}

// Options are the cluster-provided choices for the wizard/editor.
type Options struct {
	Instancetypes []Instancetype  `json:"instancetypes"`
	Preferences   []Preference    `json:"preferences"`
	OSImages      []OSImage       `json:"osImages"`
	Networks      []NetworkOption `json:"networks"`
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
