package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
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
// namespaces, narrowed by ?project= or ?namespace=. Any caller may ask; the
// rows name no host.
func (s *Server) handleProtection(w http.ResponseWriter, r *http.Request) {
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.replication.Protection(s.pvcNamespaces(r.Context(), sc.id, sc.cluster, nss)))
}

// pvcNamespaces narrows namespaces to those the caller may list claims in.
// Seeing a namespace means listing its VMs, which says nothing of its claims,
// and every row here is a claim's name and standing.
func (s *Server) pvcNamespaces(ctx context.Context, id auth.Identity, c *cluster.Client, namespaces []string) []string {
	acts := make([]action, len(namespaces))
	for i, ns := range namespaces {
		acts[i] = ask(verbList, ssarClaims, ns)
	}
	var out []string
	for i, ok := range s.capableAll(ctx, id, c, acts) {
		if ok {
			out = append(out, namespaces[i])
		}
	}
	return out
}
