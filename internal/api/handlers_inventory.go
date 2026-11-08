package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
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
	visible, err := s.visibleFor(ctx, id, userCluster)
	if err != nil {
		return model.Inventory{}, err
	}
	all := s.state.Namespaces()
	projects := s.resolver.Resolve(all, visible)

	// Live state + topology come from the SA-owned snapshot: the hot path reads
	// in-memory state, never the cluster, however many subscribers a broadcast
	// fans out to. enrich() applies live/drift only to VMs already in the user's
	// resolved projects, so the shared snapshot leaks nothing across tenants.
	in := inventory.Inputs{
		Projects: projects,
		Branch:   s.cfg.BaseBranch,
		Repos:    s.repos,
		Live:     s.state.Live(),
		Visible:  visible,
		Owners:   s.resolver.Owners(all),
	}
	// Drift comes from the SA-owned Application snapshot (lock-free, watch-fed).
	// Three states the inventory must NOT conflate: Argo off (s.drift nil: no
	// warning, Sync left unset); Argo configured but the reflector has not
	// completed its initial LIST (Drift() nil: warn, still leave Sync unset rather
	// than flashing every VM to NotTracked); and synced (the always-non-nil drift
	// map). Drift() is read once, so no window opens between the readiness check
	// and the read.
	var warnings []string
	if s.drift != nil {
		if d := s.drift.Drift(); d != nil {
			in.Drift = d
			// Same snapshot, same readiness gate: the per-project rollup covers every
			// object kind the repo declares (segments, policies, tenancy), not just VMs.
			in.ProjectDrift = s.drift.ProjectDrift()
			// Synced, but the watch is erroring: the drift shown is the last-good store.
			// Warn so a permanent ArgoCD outage doesn't masquerade as fresh sync state.
			if !s.drift.Healthy() {
				warnings = append(warnings, "sync status may be stale - ArgoCD is unreachable")
			}
		} else {
			warnings = append(warnings, "sync status is temporarily unavailable")
		}
	}
	// Same staleness contract as drift: the network catalog keeps serving its
	// last-good stores while a watch errors - warn so that isn't silent.
	if s.netstate != nil && !s.netstate.Healthy() {
		warnings = append(warnings, "the network catalog may be stale - a networking watch is failing")
	}
	if !s.state.Healthy() {
		warnings = append(warnings, "the cluster snapshot may be stale - a cluster watch is failing")
	}
	inv := inventory.Build(in)
	inv.Warnings = warnings
	var nss []string
	for _, p := range inv.Projects {
		for _, pn := range p.Namespaces {
			nss = append(nss, pn.Namespace)
		}
	}
	author := s.authorNamespaces(ctx, id, userCluster, nss)
	for i := range inv.Projects {
		for j := range inv.Projects[i].Namespaces {
			pn := &inv.Projects[i].Namespaces[j]
			if ok, known := author[pn.Namespace]; known {
				pn.CanAuthor = &ok
			}
		}
	}
	platformAuthor, platformErr := s.canAuthorPlatform(ctx, id, userCluster)
	// The platform tier has no project row; its Application rollup rides the frame
	// on its own so the issues plane names what it failed to apply or keeps
	// unhealthy. Every user sees its verdict (a user scoped to no project must not
	// mask a broken platform sync); only platform authors see its detail.
	if s.cfg.PlatformRepo != "" {
		inv.Platform = platformRollup(in.ProjectDrift, s.cfg.PlatformRepo)
		if !platformAuthor {
			inv.Platform = tenantPlatformView(inv.Platform, visible)
		}
	}
	// Propose existing tenants (namespaces with VMs, no project label) to callers
	// who could adopt them - the same namespace-create authority the create-project
	// route enforces. Rides the frame, so the list drains as adoptions merge.
	if s.cfg.PlatformRepo != "" {
		if ok, _ := s.can(ctx, id, userCluster, verbCreate, ssarNamespace, ""); ok {
			inv.Adoptable = inventory.Adoptable(in.Live, s.state.Namespaces())
		}
	}
	// The platform tier is config-only (never a labeled namespace), so it's absent
	// from `projects` - seed it for platform authors so its open PR shows on a cold
	// load, not just right after a propose tracks it.
	propProjects := projects
	if platformAuthor {
		propProjects = append(propProjects, s.platformProject())
	}
	inv.Proposals = s.proposalsFor(id, propProjects, platformErr != nil)
	// Watermark for the out-of-band network catalog: bumps when a port group moves
	// (NetworkChanged) or a non-VM object's drift content moves (ObjectDriftGen), so
	// the client re-pulls /api/networks - a merged segment, and its badge, show live.
	// The drift term is the non-VM generation, not the DriftChanged version: that
	// also moves on VM-only drift, which the frame carries itself, and counting it
	// would refetch the catalog for nothing.
	inv.NetworksVersion = s.bus.Version(eventbus.NetworkChanged)
	if s.drift != nil {
		inv.NetworksVersion += s.drift.ObjectDriftGen()
	}
	// Same contract for the recent-tasks feed: the client re-pulls /api/tasks
	// when this bumps (an op recorded, a merged PR landed).
	inv.TasksVersion = s.bus.Version(eventbus.TaskChanged)
	return inv, nil
}

// platformRollup is the platform Application's rollup out of ProjectDrift(). nil
// while Argo is off or pre-sync: off means there is no platform sync to judge,
// pre-sync already carries its own warning. An absent Application is itself the
// problem - nothing applies the platform repo - and reads as a standing error.
func platformRollup(drift map[string]model.ProjectSync, platformRepo string) *model.ProjectSync {
	if drift == nil {
		return nil
	}
	if ps, ok := drift[forge.NormalizeRepoURL(platformRepo)]; ok {
		return &ps
	}
	return &model.ProjectSync{
		Health:    "Missing",
		Operation: "Error",
		SyncError: "no ArgoCD Application sources the platform repo - the dotvirt-platform Application is missing, so tenancy changes cannot apply",
	}
}

// tenantPlatformView is the platform rollup for a caller without platform
// authority: the verdict, the unhealthy objects in namespaces they can see
// (their own Namespace objects included), and a generic line for the apply
// error, whose text can name any tenant's namespace.
func tenantPlatformView(ps *model.ProjectSync, visible map[string]bool) *model.ProjectSync {
	if ps == nil {
		return nil
	}
	out := &model.ProjectSync{Sync: ps.Sync, Health: ps.Health, Operation: ps.Operation}
	if ps.SyncError != "" {
		out.SyncError = platformErrorForTenants
	}
	for _, o := range ps.Unhealthy {
		ns := o.Namespace
		if ns == "" && o.Kind == model.KindNS.Kind {
			ns = o.Name
		}
		if visible[ns] {
			out.Unhealthy = append(out.Unhealthy, o)
		}
	}
	return out
}

const platformErrorForTenants = "the platform repo failed to apply; a platform administrator can see why"

func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	id, _, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	inv, err := s.InventoryForIdentity(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, inv)
}

// handleOptions lists the wizard/editor choices (instancetypes, preferences, OS
// images, networks, storage classes). The cluster is read once with dotvirt's SA
// and cached - a scoped tenant usually lacks cluster-scoped list on these CRDs,
// which would otherwise yield silently-empty dropdowns. The namespaced entries
// (OS images, networks) are then narrowed per caller by optionsFor, so another
// tenant's namespace and network names never leave the process.
func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	id, c, all, ok := saCached(s, w, r, s.options, (*cluster.Client).ListOptions)
	if !ok {
		return
	}
	visible, err := s.visibleFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, s.optionsFor(r.Context(), id, c, visible, all))
}

// ssarDataSources and ssarNADs are the list authorities over the namespaced
// option kinds (cluster.ListOptions reads the same resources).
var (
	ssarDataSources = ssarRef{"cdi.kubevirt.io", "datasources"}
	ssarNADs        = ssarFor(model.KindNAD)
)

// optionsFor narrows the shared catalog to what id may see: a namespaced entry
// passes when its namespace is in the caller's visible set, or when the caller
// could list that kind there itself (a shared golden-image namespace grants that
// to every authenticated user; the verdict is SSAR-cached per token). The cached
// catalog is never mutated - fresh, non-nil slices so they serialize as [].
func (s *Server) optionsFor(ctx context.Context, id auth.Identity, c *cluster.Client, visible map[string]bool, all model.Options) model.Options {
	allowed := func(ns string, ref ssarRef) bool {
		if visible[ns] {
			return true
		}
		ok, _ := s.can(ctx, id, c, verbList, ref, ns)
		return ok
	}
	out := all
	out.OSImages = make([]model.OSImage, 0, len(all.OSImages))
	for _, img := range all.OSImages {
		if allowed(img.Namespace, ssarDataSources) {
			out.OSImages = append(out.OSImages, img)
		}
	}
	out.Networks = make([]model.NetworkOption, 0, len(all.Networks))
	for _, n := range all.Networks {
		if allowed(n.Namespace, ssarNADs) {
			out.Networks = append(out.Networks, n)
		}
	}
	return out
}
