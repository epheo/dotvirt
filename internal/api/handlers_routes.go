package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/routes"
)

// handleGateways serves every Gateway and route, to a caller whose own token
// may list them across namespaces.
func (s *Server) handleGateways(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	if !s.capable(r.Context(), id, c, gatewaysRead) {
		fail(w, fmt.Errorf("%w: reading every route needs list on routes across namespaces", model.ErrForbidden))
		return
	}
	writeJSON(w, s.gateways.Overview(func(group, resource string) bool {
		return s.capable(r.Context(), id, c, ask(verbList, ssarRef{group, resource}, ""))
	}))
}

// handleRoutes serves the routes the caller may list in its namespaces,
// narrowed by ?project= or ?namespace=. Any caller may ask.
func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.gateways.Routes(s.routesReadable(r.Context(), sc.id, sc.cluster, nss)))
}

// routesReadable reviews each route kind present in namespaces. Seeing a
// namespace means listing its VMs, which says nothing of its routes.
func (s *Server) routesReadable(ctx context.Context, id auth.Identity, c *cluster.Client, namespaces []string) map[routes.Held]bool {
	held := s.gateways.HeldIn(namespaces)
	acts := make([]action, len(held))
	for i, h := range held {
		acts[i] = ask(verbList, ssarRef{routes.Group, h.Resource}, h.Namespace)
	}
	out := make(map[routes.Held]bool, len(held))
	for i, ok := range s.capableAll(ctx, id, c, acts) {
		if ok {
			out[held[i]] = true
		}
	}
	return out
}
