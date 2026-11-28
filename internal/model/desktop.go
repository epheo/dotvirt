package model

// Desktops is GET /api/desktops: the desktops' live state in the caller's
// project namespaces, each kind where the caller may list it. None of it is
// read from git.
type Desktops struct {
	// Synced is false until every served kind's first list landed, while an
	// empty list means unknown rather than none.
	Synced bool `json:"synced"`
	// Stale: a watch is down and this is its last known state.
	Stale bool `json:"stale,omitempty"`
	// Classes are shown to a caller who may list them cluster-wide.
	Classes []DesktopClass `json:"classes"`
	Pools   []DesktopPool  `json:"pools"`
	// Desktops are the ones no pool made, and those whose pool the caller is
	// not shown.
	Desktops []Desktop      `json:"desktops"`
	Claims   []DesktopClaim `json:"claims"`
	// Hidden are the namespaced kinds held where the caller may not list
	// them: a pool's count beside no desktop means desktops it may not see,
	// not none.
	Hidden []DesktopKind `json:"hidden"`
}

// DesktopKind is one namespaced kind in one namespace.
type DesktopKind struct {
	Namespace string `json:"namespace"`
	Resource  string `json:"resource"` // vdipools | vdiinstances | vdiclaims
}

// DesktopClass is a kind of desktop an admin offers.
type DesktopClass struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// DesktopPool is a set of desktops of one class that people claim.
type DesktopPool struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Class     string `json:"class"`
	Seats     int32  `json:"seats"`
	// KeepWarm is how many running desktops the pool keeps ahead of claims.
	KeepWarm   int32              `json:"keepWarm"`
	Assignment string             `json:"assignment"` // Personal | Shared
	Closed     bool               `json:"closed,omitempty"`
	Counts     DesktopPoolCounts  `json:"counts"`
	Conditions []DesktopCondition `json:"conditions,omitempty"`
	// Desktops are the ones the pool made, where the caller may list them.
	Desktops []Desktop `json:"desktops"`
}

// DesktopPoolCounts is how the pool's seats stand. Held, Warm and Free add
// up to the seats, less the desktops being deleted, unless the seats were
// lowered below what is held.
type DesktopPoolCounts struct {
	Held      int32 `json:"held"`
	InSession int32 `json:"inSession"`
	Ready     int32 `json:"ready"`
	Stopped   int32 `json:"stopped"`
	Warm      int32 `json:"warm"`
	Free      int32 `json:"free"`
	Waiting   int32 `json:"waiting"`
}

// Desktop is one streamed desktop.
type Desktop struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Class     string `json:"class,omitempty"`
	// Image is the desktop's own, empty when its class's applies.
	Image   string `json:"image,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Running bool   `json:"running"`
	Phase   string `json:"phase,omitempty"` // Provisioning | Starting | Ready | Stopping | Stopped | Failed
	Address string `json:"address,omitempty"`
	Session string `json:"session,omitempty"` // free | busy
	// Pool is the pool that made it, empty for a desktop made one by one.
	Pool string `json:"pool,omitempty"`
	// Claim is the Bound claim holding it, where the caller may list claims.
	Claim      string             `json:"claim,omitempty"`
	Conditions []DesktopCondition `json:"conditions,omitempty"`
}

// DesktopClaim is a person's claim on a desktop: through a pool, or on one
// desktop by name.
type DesktopClaim struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	User      string `json:"user"`
	// Pool is empty on a claim that names its desktop.
	Pool string `json:"pool,omitempty"`
	// Desktop is the one the claim holds, or names.
	Desktop  string `json:"desktop,omitempty"`
	Running  bool   `json:"running"`
	Phase    string `json:"phase,omitempty"` // Waiting | Bound | Refused
	Position int32  `json:"position,omitempty"`
	Message  string `json:"message,omitempty"`
	// LastUsed is when the claim got its desktop, or was last started or
	// stopped: where a pool's release counts from.
	LastUsed string `json:"lastUsed,omitempty"`
}

// DesktopCondition is one status condition, as its controller wrote it.
type DesktopCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}
