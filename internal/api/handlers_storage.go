package api

import (
	"net/http"

	"github.com/epheo/dotvirt/internal/cluster"
)

// handleStorageClasses serves the Storage section's class fact sheet. Every
// source is cluster-scoped platform truth a scoped tenant can't list itself,
// so it is read once with dotvirt's SA and cached - catalog stance, like
// /api/options. The cache is short because free capacity moves.
func (s *Server) handleStorageClasses(w http.ResponseWriter, r *http.Request) {
	_, _, classes, ok := saCached(s, w, r, s.storage, (*cluster.Client).ListStorageClasses)
	if !ok {
		return
	}
	writeJSON(w, classes)
}

// handleStorageSnapshots lists the VM snapshots across the caller's managed
// namespaces (narrowed by ?project= or ?namespace=): the Storage section's
// Snapshots tab and each disk's last snapshot. A user-opened view, read under
// the caller's token like the events lane, never from the broadcast path.
func (s *Server) handleStorageSnapshots(w http.ResponseWriter, r *http.Request) {
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	snaps, err := sc.cluster.ListVMSnapshots(r.Context(), nss)
	respond(w, snaps, err)
}

// handleStorageClaims lists the claims behind VM disks in the same scope: the
// host a local disk is bound to stays readable while its VM is stopped.
func (s *Server) handleStorageClaims(w http.ResponseWriter, r *http.Request) {
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	claims, err := sc.cluster.ListClaims(r.Context(), nss)
	respond(w, claims, err)
}
