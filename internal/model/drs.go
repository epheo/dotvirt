package model

// DRS: the descheduler-backed balance plane.

// DRSConfig is the committed DRS configuration, parsed back from the platform
// repo's KubeDescheduler manifest (defaults resolved).
type DRSConfig struct {
	Mode               string `json:"mode"`      // Predictive | Automatic
	Threshold          string `json:"threshold"` // AsymmetricLow | Low | Medium | High
	IntervalSeconds    int    `json:"intervalSeconds"`
	SoftTainter        bool   `json:"softTainter"`
	EvictionNodeLimit  int    `json:"evictionNodeLimit"`
	EvictionTotalLimit int    `json:"evictionTotalLimit"`
}

// DRSGitState is the platform repo's committed DRS state on the base branch.
type DRSGitState struct {
	Configured    bool       `json:"configured"`       // the KubeDescheduler CR is committed
	Config        *DRSConfig `json:"config,omitempty"` // nil when the committed CR doesn't parse (hand-edited)
	PSIConfigured bool       `json:"psiConfigured"`    // the PSI MachineConfig is committed
}

// DRSDraftState is the caller's pending (staged, not yet proposed) DRS change
// — the plane between committed and live that the panel's dialog edits.
type DRSDraftState struct {
	Config        *DRSConfig `json:"config,omitempty"` // the staged KubeDescheduler spec
	PSI           bool       `json:"psi,omitempty"`    // the PSI MachineConfig is staged too
	DisableStaged bool       `json:"disableStaged,omitempty"`
}

// DRSLive is the descheduler's live state, read from the SA-watched
// KubeDescheduler snapshot — never the cluster per-request.
type DRSLive struct {
	// APIPresent: the Kube Descheduler Operator's CRD is served. False on a
	// cluster where the operator was never installed — the "not installed" state
	// the panel shows until the first enable-PR merges and OLM installs it.
	APIPresent bool `json:"apiPresent"`
	// Synced: the initial LIST landed; until then Deployed=false means
	// "unknown", not "absent". Stale: the API is served but the watch is
	// currently failing (e.g. RBAC not yet reconciled, apiserver outage) — the
	// live fields may be missing or outdated.
	Synced bool `json:"synced"`
	Stale  bool `json:"stale,omitempty"`
	// Deployed: a KubeDescheduler CR exists in the cluster.
	Deployed        bool     `json:"deployed"`
	ManagementState string   `json:"managementState,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	Profiles        []string `json:"profiles,omitempty"`
	IntervalSeconds int64    `json:"intervalSeconds,omitempty"`
	// Available mirrors the operator's Available condition; Degraded carries the
	// Degraded condition's message when that condition is true.
	Available bool   `json:"available"`
	Degraded  string `json:"degraded,omitempty"`
}

// DRSView is GET /api/drs: the DRS tier across its planes — the committed git
// state (flattened), the caller's staged draft, the live operator state — plus
// the caller's authoring capability, the same SSARs the POST/DELETE handlers
// enforce. Warning carries a non-fatal degradation (e.g. the platform repo is
// unreachable, so the committed state is unknown) instead of failing the view.
type DRSView struct {
	// The committed git plane: DRSGitState's fields spelled out (same json tags)
	// because tygo renders an embedded struct as a nested field, not flattened.
	Configured    bool       `json:"configured"`
	Config        *DRSConfig `json:"config,omitempty"`
	PSIConfigured bool       `json:"psiConfigured"`

	Draft     *DRSDraftState `json:"draft,omitempty"`
	Live      DRSLive        `json:"live"`
	Warning   string         `json:"warning,omitempty"`
	CanManage bool           `json:"canManage"` // kubedeschedulers-create — gates the panel's actions
	CanPSI    bool           `json:"canPSI"`    // machineconfigs-create — gates the PSI checkbox
}
