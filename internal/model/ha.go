package model

// HA: the Medik8s node-remediation plane - host fencing + automatic VM
// restart on host failure.

// HAConfig is the committed HA configuration, parsed back from the platform
// repo's NodeHealthCheck manifest (defaults resolved).
type HAConfig struct {
	UnhealthySeconds  int `json:"unhealthySeconds"`  // detection patience before fencing
	MinHealthyPercent int `json:"minHealthyPercent"` // remediation storm brake
}

// HAGitState is the platform repo's committed HA state on the base branch.
type HAGitState struct {
	Configured bool      `json:"configured"`       // the NodeHealthCheck CR is committed
	Config     *HAConfig `json:"config,omitempty"` // nil when the committed CR doesn't parse (hand-edited)
}

// HADraftState is the caller's pending (staged, not yet proposed) HA change -
// the plane between committed and live that the panel's dialog edits.
type HADraftState struct {
	Config        *HAConfig `json:"config,omitempty"` // the staged NodeHealthCheck spec
	DisableStaged bool      `json:"disableStaged,omitempty"`
}

// HALive is the Node Health Check operator's live state, read from the
// SA-watched NodeHealthCheck snapshot - never the cluster per-request.
type HALive struct {
	// APIPresent: the NodeHealthCheck CRD is served. False on a cluster where
	// the operator was never installed - the "not installed" state the panel
	// shows until the first enable-PR merges and OLM installs it.
	APIPresent bool `json:"apiPresent"`
	// Synced: the initial LIST landed; until then Deployed=false means
	// "unknown", not "absent". Stale: the API is served but the watch is
	// currently failing - the live fields may be missing or outdated.
	Synced bool `json:"synced"`
	Stale  bool `json:"stale,omitempty"`
	// Deployed: a NodeHealthCheck CR exists in the cluster.
	Deployed bool `json:"deployed"`
	// Phase is the operator's own rollup (Enabled | Disabled | Paused |
	// Remediating); Reason says why when it disables itself (e.g. a
	// conflicting machine-api MachineHealthCheck).
	Phase  string `json:"phase,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Healthy/observed worker counts; 0/0 until the operator reports.
	ObservedNodes int64 `json:"observedNodes"`
	HealthyNodes  int64 `json:"healthyNodes"`
	// Remediating lists the hosts being fenced right now, sorted.
	Remediating []string `json:"remediating,omitempty"`
}

// HAView is GET /api/ha: the HA tier across its planes - the committed git
// state (flattened), the caller's staged draft, the live operator state - plus
// the caller's authoring capability, the same SSAR the POST/DELETE handlers
// enforce. Warning carries a non-fatal degradation (e.g. the platform repo is
// unreachable, so the committed state is unknown) instead of failing the view.
type HAView struct {
	// The committed git plane: HAGitState's fields spelled out (same json tags)
	// because tygo renders an embedded struct as a nested field, not flattened.
	Configured bool      `json:"configured"`
	Config     *HAConfig `json:"config,omitempty"`

	Draft     *HADraftState `json:"draft,omitempty"`
	Live      HALive        `json:"live"`
	Warning   string        `json:"warning,omitempty"`
	CanManage bool          `json:"canManage"` // nodehealthchecks-create - gates the panel's actions
}
