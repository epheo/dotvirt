package api

import (
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
)

// handleStorageClasses serves the Storage section's class fact sheet. Every
// source is cluster-scoped platform truth a scoped tenant can't list itself,
// so it is read once with dotvirt's SA and cached - catalog stance, like
// /api/options. The cache is short because free capacity moves.
func (s *Server) handleStorageClasses(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.FromContext(r.Context()); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if s.clusterF == nil {
		http.Error(w, "cluster not configured", http.StatusServiceUnavailable)
		return
	}
	classes, ok := s.storage.Get("all")
	if !ok {
		sa, err := s.clusterF.SA()
		if err != nil {
			fail(w, unavailable("cluster access", err))
			return
		}
		classes, err = sa.ListStorageClasses(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		s.storage.Put("all", classes)
	}
	writeJSON(w, http.StatusOK, classes)
}
