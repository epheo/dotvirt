// Package model holds the API-facing types shared across dotvirt's planes.
package model

import "errors"

// Error kinds the domain (e.g. changeset) can wrap so the HTTP layer maps them to
// the right status instead of a blanket 500. Wrap with fmt.Errorf("%w: ...", kind).
var (
	ErrInvalid     = errors.New("invalid request")         // -> 400: bad/empty input, nothing to do
	ErrNotFound    = errors.New("not found")               // -> 404
	ErrForbidden   = errors.New("forbidden")               // -> 403: caller lacks authority for the operation
	ErrConflict    = errors.New("conflict")                // -> 409: e.g. project not editable
	ErrUnavailable = errors.New("temporarily unavailable") // -> 503: a capability isn't wired/reachable
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
	SyncNotTracked SyncStatus = "NotTracked" // lives in the cluster only; git does not describe it
	// Declared in git but no ArgoCD Application reports it yet - the window
	// between a merged adoption/create and the ApplicationSet + first sync
	// catching up. NotTracked here would read as "adoption failed".
	SyncPending SyncStatus = "Pending"
	SyncUnknown SyncStatus = "Unknown"
)
