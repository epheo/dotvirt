package api

import "net/http"

// handlePermissions returns the caller's effective capabilities in one
// namespace (the Permissions tab). resolveProject gates the namespace the same
// way every VM route does, so a user can't probe namespaces outside their
// projects; the SelfSubjectRulesReview then runs under their own token.
func (s *Server) handlePermissions(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		http.Error(w, "namespace query parameter is required", http.StatusBadRequest)
		return
	}
	sc, ok := s.resolveProject(w, r, byNamespace(ns))
	if !ok {
		return
	}
	p, err := sc.cluster.Permissions(r.Context(), ns)
	respond(w, p, err)
}
