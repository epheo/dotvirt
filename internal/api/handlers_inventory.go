package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/inventory"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/forge"
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

	// Live state + topology come from the SA-owned snapshot: the hot path reads
	// in-memory state, never the cluster, however many subscribers a broadcast
	// fans out to. enrich() applies live/drift only to VMs already in the user's
	// resolved projects, so the shared snapshot leaks nothing across tenants.
	in := inventory.Inputs{
		Projects: projects,
		Branch:   s.cfg.BaseBranch,
		Repos:    s.repos,
		Live:     s.state.LiveVMs(),
	}
	// Drift comes from the SA-owned Application snapshot (lock-free, watch-fed).
	// Distinguish three states the inventory must NOT conflate: Argo off (s.drift
	// nil -> no warning, Sync left unset); Argo configured but the reflector hasn't
	// completed its initial LIST (s.drift.Drift() is nil -> surface a warning, still
	// leave Sync unset rather than flashing every VM to NotTracked); and synced
	// (apply the always-non-nil drift map).
	var warnings []string
	if s.drift != nil {
		// Drift() is nil exactly while Argo is configured but its reflector hasn't
		// finished the initial LIST - surface that as a degradation (Sync left unset,
		// not a flash of NotTracked); once synced it's an always-non-nil map. One call,
		// so there's no window between a readiness check and the read.
		if d := s.drift.Drift(); d != nil {
			in.Drift = d
			// Same snapshot, same readiness gate: the per-project rollup covers every
			// object kind the repo declares (segments, policies, tenancy), not just VMs.
			in.ProjectDrift = s.drift.ProjectDrift()
			// Synced, but the watch is erroring: the drift shown is the last-good store.
			// Warn so a permanent ArgoCD outage doesn't masquerade as fresh sync state.
			if !s.drift.Healthy() {
				warnings = append(warnings, "sync status may be stale — ArgoCD is unreachable")
			}
		} else {
			warnings = append(warnings, "sync status is temporarily unavailable")
		}
	}
	// Same staleness contract as drift: the network catalog keeps serving its
	// last-good stores while a watch errors - warn so that isn't silent.
	if s.netstate != nil && !s.netstate.Healthy() {
		warnings = append(warnings, "the network catalog may be stale — a networking watch is failing")
	}
	// Zero project namespaces with a platform repo configured is either a pristine
	// install (fine, the empty state is correct) or a platform app that stopped
	// applying (broken). The platform Application's own rollup - already in the
	// drift snapshot - tells them apart, so warn only on actual breakage and name
	// it. Surfaced cluster-wide (the snapshot is unfiltered) so a user legitimately
	// scoped to no project can't mask a broken sync.
	if s.cfg.PlatformRepo != "" && len(s.state.Namespaces()) == 0 {
		if w := platformSyncWarning(in.ProjectDrift, s.cfg.PlatformRepo); w != "" {
			warnings = append(warnings, w)
		}
	}
	inv := inventory.Build(in)
	inv.Warnings = warnings
	// The platform tier is config-only (never a labeled namespace), so it's absent
	// from `projects` - seed it for platform authors so its open PR shows on a cold
	// load, not just right after a propose tracks it.
	propProjects := projects
	if s.canAuthorPlatform(ctx, id, userCluster) {
		propProjects = append(propProjects, s.platformProject())
	}
	inv.Proposals = s.proposalsFor(id, propProjects)
	// Watermark for the out-of-band network catalog: bumps when a port group moves
	// (NetworkChanged) or a non-VM object's drift content moves (ObjectDriftGen), so
	// the client re-pulls /api/networks - a merged segment, and its badge, show live.
	// The drift term is the CONTENT generation, not the raw DriftChanged version:
	// Application objects churn on every reconcile (reconciledAt), and a version that
	// counted that would defeat the hub's identical-frame suppression and refetch the
	// catalog for nothing.
	inv.NetworksVersion = s.bus.Version(eventbus.NetworkChanged)
	if s.drift != nil {
		inv.NetworksVersion += s.drift.ObjectDriftGen()
	}
	// Same contract for the recent-tasks feed: the client re-pulls /api/tasks
	// when this bumps (an op recorded, a merged PR landed).
	inv.TasksVersion = s.bus.Version(eventbus.TaskChanged)
	return inv, nil
}

// platformSyncWarning decides what "zero project namespaces" means from the
// platform Application's rollup. drift is ProjectDrift(): nil while Argo is off
// or pre-sync - stay quiet; off means there is no platform sync to be unhealthy,
// pre-sync already carries its own warning. Progressing/Running are quiet too:
// the first sync after an install is not a degradation.
func platformSyncWarning(drift map[string]model.ProjectSync, platformRepo string) string {
	if drift == nil {
		return ""
	}
	ps, ok := drift[forge.NormalizeRepoURL(platformRepo)]
	if !ok {
		return "no projects found and no ArgoCD Application sources the platform repo — the dotvirt-platform Application is missing"
	}
	okHealth := ps.Health == "" || ps.Health == "Healthy" || ps.Health == "Progressing"
	failedOp := ps.Operation == "Failed" || ps.Operation == "Error"
	if okHealth && !failedOp && ps.SyncError == "" {
		return "" // genuinely no projects yet
	}
	msg := "the platform GitOps sync is unhealthy (dotvirt-platform Application)"
	if ps.SyncError != "" {
		msg += ": " + ps.SyncError
	}
	return msg
}

func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	id, _, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return
	}
	inv, err := s.InventoryForIdentity(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// handleOptions lists the wizard/editor choices (instancetypes, preferences, OS
// images, networks). These are cluster catalog data, the same for every tenant, so
// they're read with dotvirt's SA - a scoped tenant usually lacks cluster-scoped
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
		fail(w, unavailable("cluster access", err))
		return
	}
	opts, err := sa.ListOptions(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	s.options.Put("all", opts)
	writeJSON(w, http.StatusOK, opts)
}
