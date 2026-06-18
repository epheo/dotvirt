package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/inventory"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// InventoryForIdentity builds the multi-tenant inventory visible to id: resolve
// the user's projects, gather their live + drift view under the user's token, and
// assemble. Exported so the WebSocket hub (per subscriber) reuses the exact same
// path as GET /api/inventory.
func (s *Server) InventoryForIdentity(ctx context.Context, id auth.Identity) (model.Inventory, error) {
	if s.clusterF == nil {
		// The WS hub calls this directly (bypassing userCluster's guard); fail closed
		// rather than nil-deref the factory and crash the hub goroutine.
		return model.Inventory{}, fmt.Errorf("cluster not configured")
	}
	userCluster, err := s.clusterF.For(id.Token)
	if err != nil {
		return model.Inventory{}, err
	}
	projects, err := s.projectsFor(ctx, id, userCluster)
	if err != nil {
		return model.Inventory{}, err
	}

	// Live state + topology come from the SA-owned snapshot (in-memory, no cluster
	// call), so a broadcast to N subscribers no longer issues N×(LIST+GET) — the
	// throttling that wedged the server is gone. enrich() applies live/drift only to
	// VMs already in the user's resolved projects, so the shared snapshot leaks
	// nothing across tenants.
	in := inventory.Inputs{
		Projects: projects,
		Branch:   s.cfg.BaseBranch,
		Repos:    s.repos,
		Live:     s.state.LiveVMs(),
	}
	// Drift is TTL-cached + shared across subscribers. A nil cache means Argo is
	// intentionally off (no warning); a non-nil cache that errors is a degradation
	// worth surfacing.
	var warnings []string
	if s.drift != nil {
		if drift, err := s.drift.Get(ctx); err == nil {
			in.Drift = drift // non-nil (VMDrift always returns a map) ⇒ drift enabled
		} else {
			warnings = append(warnings, "sync status is temporarily unavailable")
		}
	}
	inv := inventory.Build(in)
	inv.Warnings = warnings
	// The platform tier is config-only (never a labeled namespace), so it's absent
	// from `projects` — seed it for platform authors so its open PR shows on a cold
	// load, not just right after a propose tracks it.
	propProjects := projects
	if s.canAuthorPlatform(ctx, id, userCluster) {
		propProjects = append(propProjects, project.ProjectInfo{Name: platformProjectName, Repo: s.cfg.PlatformRepo})
	}
	inv.Proposals = s.proposalsFor(id, propProjects)
	return inv, nil
}

func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	id, _, err := s.userCluster(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	inv, err := s.InventoryForIdentity(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// handleOptions lists the wizard/editor choices (instancetypes, preferences, OS
// images, networks). These are cluster catalog data, the same for every tenant, so
// they're read with dotvirt's SA — a scoped tenant usually lacks cluster-scoped
// list on these CRDs, which would otherwise yield silently-empty dropdowns. The
// caller must still be authenticated (middleware) to reach here.
func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.FromContext(r.Context()); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	// Catalog is identical for everyone; serve the shared cache to skip 4 cluster
	// LISTs per wizard open.
	if v, ok := s.options.Get("all"); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}
	if s.clusterF == nil {
		http.Error(w, "cluster not configured", http.StatusServiceUnavailable)
		return
	}
	sa, err := s.clusterF.SA()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	opts, err := sa.ListOptions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.options.Put("all", opts)
	writeJSON(w, http.StatusOK, opts)
}
