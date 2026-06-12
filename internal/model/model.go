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

	// From cluster (actual state), when cluster reads are enabled.
	Phase        string   `json:"phase,omitempty"`  // VMI phase, e.g. Running
	Paused       bool     `json:"paused,omitempty"` // VMI Paused condition (phase stays Running)
	GuestIP      string   `json:"guestIP,omitempty"`
	IPs          []string `json:"ips,omitempty"` // every guest-reported IP
	NodeName     string   `json:"nodeName,omitempty"`
	OS           string   `json:"os,omitempty"`           // guest-agent OS pretty name
	MemoryActual string   `json:"memoryActual,omitempty"` // current guest memory (hotplug-aware)
	StartedAt    string   `json:"startedAt,omitempty"`    // RFC3339; VMI entered Running (for uptime)

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

	SetLabels      map[string]string `json:"setLabels,omitempty"`
	RemoveLabels   []string          `json:"removeLabels,omitempty"`
	AddDisks       []DiskAdd         `json:"addDisks,omitempty"`
	RemoveDisks    []string          `json:"removeDisks,omitempty"`
	AddNetworks    []NetworkAdd      `json:"addNetworks,omitempty"`
	RemoveNetworks []string          `json:"removeNetworks,omitempty"`

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

// Proposal is an open pull request backing a project's draft — the staged→PR→
// synced lifecycle's middle state, surfaced as a Recent Tasks row.
type Proposal struct {
	Project  string `json:"project"`
	PRNumber int    `json:"prNumber"`
	PRURL    string `json:"prURL"`
	Title    string `json:"title,omitempty"`
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
