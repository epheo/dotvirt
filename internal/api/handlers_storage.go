package api

import (
	"net/http"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// handleStorageClasses serves the Storage section's class fact sheet. Every
// source is cluster-scoped platform truth a scoped tenant can't list itself,
// so it is read once with dotvirt's SA and cached - catalog stance, like
// /api/options. The cache is short because free capacity moves.
//
// A capacity segment's topology is usually a node's hostname, so the
// per-segment split shows only to callers who may read nodes; everyone else
// gets the class's summed free capacity.
func (s *Server) handleStorageClasses(w http.ResponseWriter, r *http.Request) {
	id, c, classes, ok := saCached(s, w, r, s.storage, (*cluster.Client).ListStorageClasses)
	if !ok {
		return
	}
	if canNodes, _ := s.can(r.Context(), id, c, refQ(verbList, ssarNodes, "")); !canNodes {
		classes = withoutSegments(classes)
	}
	writeJSON(w, classes)
}

// withoutSegments copies the shared cached sheet without the per-segment
// split, leaving the summed free capacity.
func withoutSegments(classes []model.StorageClassInfo) []model.StorageClassInfo {
	out := make([]model.StorageClassInfo, len(classes))
	for i, c := range classes {
		c.Segments = nil
		out[i] = c
	}
	return out
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
