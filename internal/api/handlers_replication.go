package api

import (
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/model"
)

// handleReplication serves where every copy lives, to a caller whose own token
// may list what the answer shows.
func (s *Server) handleReplication(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	if !s.capable(r.Context(), id, c, replicationRead) {
		fail(w, fmt.Errorf("%w: reading replicated storage's placement needs list on its cluster-scoped kinds", model.ErrForbidden))
		return
	}
	writeJSON(w, s.replication.Overview())
}

// handleProtection serves the standing of the replicated disks in the caller's
// namespaces, narrowed by ?project= or ?namespace=. Any caller may ask: the
// scope is the filter and the rows name no host.
func (s *Server) handleProtection(w http.ResponseWriter, r *http.Request) {
	_, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.replication.Protection(nss))
}
