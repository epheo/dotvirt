package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/inventory"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
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
	// A stream outlives the request that authenticated it, and its cached
	// reviews can outlive a revoked token by minutes: re-check it on the
	// authenticator's cache, shared with the session's REST calls, so a dead
	// token ends its stream at the next frame past that cache's TTL. A failed
	// re-check withholds the frame without ending the session. A signed-out or
	// expired session fails it too, and a rejection closes the token's consoles.
	if s.auth != nil {
		if err := s.auth.Revalidate(ctx, id); errors.Is(err, auth.ErrRejected) {
			s.bus.Publish(eventbus.SessionEnded)
			if errors.Is(err, auth.ErrSessionEnded) {
				return model.Inventory{}, err // this session's end, not its token's
			}
			return model.Inventory{}, errSessionRejected
		} else if err != nil {
			return model.Inventory{}, unavailable(err)
		}
	}
	userCluster, err := s.clusterF.For(id.Token)
	if err != nil {
		return model.Inventory{}, err
	}
	v, err := s.viewFor(ctx, id, userCluster)
	if err != nil {
		return model.Inventory{}, err
	}
	visible := v.names
	all := s.state.Namespaces()
	projects := s.resolver.Resolve(all, visible)

	// Live state + topology come from the SA-owned snapshot: the hot path reads
	// in-memory state, never the cluster, however many subscribers a broadcast
	// fans out to. enrich() applies live/drift only to VMs already in the user's
	// resolved projects, so the shared snapshot leaks nothing across tenants.
	in := inventory.Inputs{
		Projects:  projects,
		Branch:    s.cfg.BaseBranch,
		Repos:     s.repos,
		Live:      s.state.Live(),
		Visible:   visible,
		Owners:    s.resolver.Owners(all),
		ListsVMs:  func(ns string) bool { return v.lists(ssarVM, ns) },
		ListsVMIs: func(ns string) bool { return v.lists(ssarVMI, ns) },
		ScopeSync: func(p project.ProjectInfo, ps model.ProjectSync) *model.ProjectSync {
			return s.projectSyncView(ctx, id, userCluster, p, ps)
		},
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
			// object kind the repo declares (networks, policies, tenancy), not just VMs.
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
	// The platform tier has no project row; its Application rollup rides the frame
	// on its own so the issues plane names what it failed to apply or keeps
	// unhealthy. Only platform readers (canReadPlatform) see its verdict, detail,
	// open proposals and merges: the verdict moves with other tenants' changes.
	// Anyone else gets the rows about objects Kubernetes shows them, and their
	// own draft's proposal and merges.
	var platformAuthor bool
	var platformErr error
	if s.cfg.PlatformRepo != "" {
		rollup := platformRollup(in.ProjectDrift, s.cfg.PlatformRepo)
		if s.platformBusy() || rollup != nil {
			platformAuthor, platformErr = s.canReadPlatform(ctx, id, userCluster)
		}
		if platformAuthor {
			inv.Platform = s.readerPlatformView(ctx, id, userCluster, rollup)
		} else {
			inv.Platform = s.tenantPlatformView(ctx, id, userCluster, visible, rollup)
		}
	}
	// Propose existing tenants (namespaces with VMs, no project label) to callers
	// who could make one a project: updating namespaces cluster-wide answers
	// for each one the create-project route claims, and for its Namespace
	// object, which the coordinator's dry run finds running. Rides the frame,
	// so the list drains as adoptions merge.
	if s.cfg.PlatformRepo != "" {
		if s.capable(ctx, id, userCluster, claimAction("")) && s.capable(ctx, id, userCluster, ask(verbList, ssarVM, "")) &&
			s.capable(ctx, id, userCluster, ask(verbList, ssarNamespace, "")) {
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
	// Watermarks for the out-of-band pulls, scoped to what this caller's pulls
	// read (watermark.go): a merged network and its badge, or a recorded op,
	// show live without another tenant's act ever moving this frame. The
	// verdicts that shape the pulls (netGrants) shape the watermarks too.
	grants := s.netGrants(ctx, id, userCluster, platformAuthor)
	inv.NetworksVersion = s.networksWatermark(v, grants)
	inv.TasksVersion = s.tasksWatermark(s.taskScope(id, projects, v, platformAuthor, grants.nodes))
	inv.ReplicationVersion = s.replicationWatermark(ctx, id, userCluster, v)
	inv.RoutesVersion = s.routesWatermark(ctx, id, userCluster, visible)
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

// platformBusy reports whether the platform tier holds something a frame
// shows only to its authors beyond its rollup: open proposals (the refresher
// watches the platform repo whenever anyone looks) or merges in the task feed.
func (s *Server) platformBusy() bool {
	rows, ok := s.proposals.Get(platformProjectName)
	if ok && len(rows) > 0 {
		return true
	}
	if !ok {
		s.nudgeProposals("")
	}
	if s.tasks == nil {
		return false
	}
	repo, busy := forge.NormalizeRepoURL(s.cfg.PlatformRepo), false
	s.tasks.Visit(func(tasks.Op) {}, func(m tasks.Merge) { busy = busy || m.RepoURL == repo })
	return busy
}

// tenantPlatformView is the platform rollup for a caller without platform
// read: only the rows about objects Kubernetes shows them, their own Namespace
// objects included. No verdict or apply error: those move with, and can name,
// other tenants.
func (s *Server) tenantPlatformView(ctx context.Context, id auth.Identity, c *cluster.Client, visible map[string]bool, ps *model.ProjectSync) *model.ProjectSync {
	namespaces, namespaced := tenantPlatformRows(ps, visible)
	listed := s.listedRows(ctx, id, c, model.ProjectSync{Unhealthy: namespaced}, func(o model.ObjectHealth) string { return o.Namespace }, "")
	rows := append(namespaces, listed.Unhealthy...)
	if len(rows) == 0 {
		return nil
	}
	return &model.ProjectSync{Unhealthy: rows}
}

// tenantPlatformRows splits ps's rows in visible namespaces: the Namespace
// objects themselves, which seeing the namespace answers, and the objects
// inside, whose kind is still to ask.
func tenantPlatformRows(ps *model.ProjectSync, visible map[string]bool) (namespaces, namespaced []model.ObjectHealth) {
	if ps == nil {
		return nil, nil
	}
	for _, o := range ps.Unhealthy {
		switch {
		case o.Namespace == "" && o.Kind == model.KindNS.Kind && visible[o.Name]:
			namespaces = append(namespaces, o)
		case o.Namespace != "" && visible[o.Namespace]:
			namespaced = append(namespaced, o)
		}
	}
	return namespaces, namespaced
}

const platformErrorForTenants = "the platform repo failed to apply; a platform administrator can see why"

// readerPlatformView is the platform rollup for a platform reader: the rows
// of kinds they may list cluster-wide. Platform read weighs the kinds git
// declares, and Argo's live tree can hold others for a while (a mirror behind
// a merge, a stuck prune, a List Argo unwraps), so each row's kind is asked
// on its own, a kind discovery cannot place read as unlisted. The apply error
// can name a dropped row, so it turns generic once one is dropped.
func (s *Server) readerPlatformView(ctx context.Context, id auth.Identity, c *cluster.Client, ps *model.ProjectSync) *model.ProjectSync {
	if ps == nil {
		return nil
	}
	return s.listedRows(ctx, id, c, *ps, func(model.ObjectHealth) string { return "" }, platformErrorForTenants)
}

const projectErrorForPartialViewers = "this project's repo failed to apply; someone who can read all of it can see why"

// projectSyncView is a tenant project's rollup for one caller: the rows it may
// list where they live. The apply error can name what it may not, so it turns
// generic once a row is dropped or the caller sees part of the project.
func (s *Server) projectSyncView(ctx context.Context, id auth.Identity, c *cluster.Client, p project.ProjectInfo, ps model.ProjectSync) *model.ProjectSync {
	out := s.listedRows(ctx, id, c, ps, func(o model.ObjectHealth) string { return o.Namespace }, projectErrorForPartialViewers)
	if out.SyncError != "" && s.seesPartOf(p) {
		out.SyncError = projectErrorForPartialViewers
	}
	return out
}

// listedRows keeps the Unhealthy rows whose kind the caller may list in
// where(row). Each kind is asked on its own, cached per token, and only when a
// row exists: a quiet rollup costs nothing. A kind discovery cannot place reads
// as unlisted. The apply error can name a dropped row, so it becomes generic
// once one is dropped.
func (s *Server) listedRows(ctx context.Context, id auth.Identity, c *cluster.Client, ps model.ProjectSync, where func(model.ObjectHealth) string, generic string) *model.ProjectSync {
	if len(ps.Unhealthy) == 0 {
		return &ps
	}
	var kinds []schema.GroupKind
	for _, o := range ps.Unhealthy {
		if k := (schema.GroupKind{Group: o.Group, Kind: o.Kind}); !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	x, _ := s.servedKinds(ctx, kinds)
	out := ps
	out.Unhealthy = nil
	for _, o := range ps.Unhealthy {
		ref, served, err := x.ref(schema.GroupKind{Group: o.Group, Kind: o.Kind})
		if err == nil && served && s.capable(ctx, id, c, ask(verbList, ref, where(o))) {
			out.Unhealthy = append(out.Unhealthy, o)
		}
	}
	if len(out.Unhealthy) < len(ps.Unhealthy) && out.SyncError != "" {
		out.SyncError = generic
	}
	return &out
}

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
	v, err := s.viewFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, optionsFor(v, all))
}

// optionsFor narrows the shared catalog to what the caller may list: outside
// the view too, where a binding names them (a shared golden-image namespace
// grants that to every authenticated user). The cached catalog is never
// mutated - fresh, non-nil slices so they serialize as [].
func optionsFor(v callerView, all model.Options) model.Options {
	allowed := func(ns string, ref ssarRef) bool { return v.lists(ref, ns) }
	out := all
	out.OSImages = make([]model.OSImage, 0, len(all.OSImages))
	for _, img := range all.OSImages {
		if allowed(img.Namespace, ssarDataSources) {
			out.OSImages = append(out.OSImages, img)
		}
	}
	out.Networks = make([]model.NetworkOption, 0, len(all.Networks))
	for _, n := range all.Networks {
		if allowed(n.Namespace, ssarFor(model.KindNAD)) {
			out.Networks = append(out.Networks, n)
		}
	}
	return out
}
