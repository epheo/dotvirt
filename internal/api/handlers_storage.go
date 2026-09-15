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
	writeJSON(w, http.StatusOK, classes)
}
