package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
)

// The networking create routes resolve their target TIER from the object's SCOPE,
// never from a client-supplied repo: a namespace-scoped UDN goes to the tenant's own
// project repo; cluster-scoped objects (CUDN, NNCP uplink, Namespace) go to the
// platform repo. A create form also edits the object git already declares, so the
// right to create or update the kind opens it, and the coordinator then checks
// the exact one.
// The single-scope routes are built by the two factories below and registered in
// api.go, where each route's authorization rationale lives; only the routes with
// per-request scope or SSAR switches keep bespoke handlers here.

type nsPeek struct {
	Namespace string `json:"namespace"`
}

// platformCreate builds the handler for a cluster-scoped create form on kind:
// always the platform tier, opened by the right to create or update the kind;
// the coordinator checks the one the rendered form needs.
func (s *Server) platformCreate(kind model.Kind, resource draft.Resource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readRaw(w, r)
		if !ok {
			return
		}
		sc, ok := s.platformForm(w, r, kind)
		if !ok {
			return
		}
		view, err := s.draft.StageCreate(sc.id, sc.proj, resource, raw)
		respond(w, view, err)
	}
}

// namespacedCreate builds the handler for a namespace-scoped create form on
// kind: it peeks the spec's namespace and routes to the tenant project owning
// it (resolveProject is the authorization point - a namespace outside the
// caller's projects is not found), then gates like platformCreate. what names
// the kind in the missing-namespace error, article included.
func (s *Server) namespacedCreate(what string, resource draft.Resource, kind model.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, p, ok := readBody[nsPeek](w, r, false)
		if !ok {
			return
		}
		if p.Namespace == "" {
			fail(w, invalid(fmt.Errorf("a namespace is required for %s", what)))
			return
		}
		sc, ok := s.resolveProject(w, r, byNamespace(p.Namespace))
		if !ok || !s.gateForm(w, r, sc, kind, p.Namespace) {
			return
		}
		view, err := s.draft.StageCreate(sc.id, sc.proj, resource, raw)
		respond(w, view, err)
	}
}

// handleCreateNetwork stages a new Distributed Port Group, proposed as a PR (the
// same owns-nothing path as a VM create). A "project"-scoped Layer2 secondary UDN
// lands in the tenant repo owning its namespace; a shared/VLAN CUDN is
// cluster-scoped, so it routes to the platform tier.
func (s *Server) handleCreateNetwork(w http.ResponseWriter, r *http.Request) {
	raw, p, ok := readBody[struct {
		Scope     string `json:"scope"`
		Namespace string `json:"namespace"`
	}](w, r, false)
	if !ok {
		return
	}
	var sc scope
	switch p.Scope {
	case "", netgen.ScopeProject:
		if p.Namespace == "" {
			fail(w, invalid(errors.New("a namespace is required for a project-scoped network")))
			return
		}
		sc, ok = s.resolveProject(w, r, byNamespace(p.Namespace))
		ok = ok && s.gateForm(w, r, sc, model.KindUDN, p.Namespace)
	default: // ScopeShared / ScopeVLAN - a cluster-scoped CUDN
		sc, ok = s.platformForm(w, r, model.KindCUDN)
	}
	if !ok {
		return
	}
	view, err := s.draft.StageCreate(sc.id, sc.proj, draft.ResourceNetwork, raw)
	respond(w, view, err)
}

// handleCreateAdminNetworkPolicy stages a cluster-wide admin DFW policy
// (AdminNetworkPolicy or the baseline default) - always platform-tier and admin-only,
// gated like platformCreate on the matching kind. A priority a live policy
// already holds is refused: OVN-K would order the two arbitrarily.
func (s *Server) handleCreateAdminNetworkPolicy(w http.ResponseWriter, r *http.Request) {
	raw, p, ok := readBody[struct {
		Name     string `json:"name"`
		Baseline bool   `json:"baseline"`
		Priority int    `json:"priority"`
	}](w, r, false)
	if !ok {
		return
	}
	kind := model.KindANP
	if p.Baseline {
		kind = model.KindBANP
	}
	sc, ok := s.platformForm(w, r, kind)
	if !ok {
		return
	}
	if !p.Baseline && s.netstate != nil {
		if other, taken := s.netstate.ANPAtPriority(p.Priority, p.Name); taken {
			fail(w, fmt.Errorf("%w: priority %d is held by admin network policy %q; pick a free one, equal priorities apply in an undefined order",
				model.ErrConflict, p.Priority, other))
			return
		}
	}
	view, err := s.draft.StageCreate(sc.id, sc.proj, draft.ResourceAdminNetworkPolicy, raw)
	respond(w, view, err)
}

// handleNetworks lists the networks (Distributed Port Groups) the caller may
// attach a VM to, plus the physical fabric (Uplinks + Physical adapters) for
// callers who can read nodes. The port-group catalog is read with dotvirt's SA -
// a scoped tenant usually can't list these cluster CRDs, which would yield a
// silently-empty picker - then project-scoped networks are filtered to the
// caller's visible namespaces so nothing leaks across tenants.
func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}

	// The full catalog is a lock-free scan of the SA-maintained netstate snapshot
	// (watch-fed, identical for everyone) - no per-request cluster LIST. Per-tenant
	// scoping and per-object drift happen below, off this copy. Nil netstate (a
	// stripped-down wiring) degrades to an empty catalog, per the Deps contract.
	var full model.NetworkInventory
	if s.netstate != nil {
		full = s.netstate.Catalog()
	}

	visible, err := s.visibleFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return
	}

	out := model.NetworkInventory{
		Networks:         scopeNetworks(full.Networks, visible),
		Uplinks:          []model.Uplink{},
		PhysicalAdapters: []model.PhysicalAdapter{},
		NMStatePresent:   full.NMStatePresent,
	}
	ctx := r.Context()
	can := func(k model.Kind) bool { return s.capable(ctx, id, c, kindAction(verbCreate, k, "")) }
	cudn := can(model.KindCUDN)
	// Per-object drift: attach each segment's own ArgoCD sync/health at serve time
	// (always fresh, off the cached catalog) - the same surface VMs carry.
	s.enrichNetworkDrift(out.Networks)
	if !cudn {
		redactSharedErrors(out.Networks)
	}
	// Per-action authoring authority: the same SSARs the create handlers enforce, so
	// the UI gates each button precisely. CanManage stays the coarse CUDN signal that
	// gates the platform-draft view. All false when no platform repo is configured.
	if s.cfg.PlatformRepo != "" {
		out.CanManage = cudn
		out.Caps = model.NetworkCaps{
			Uplink:                     can(model.KindNNCP),
			Namespace:                  s.newProjectCap(ctx, id, c, `{"name":"`+newNamespace+`"}`),
			EgressIP:                   can(model.KindEgressIP),
			ExternalRoute:              can(model.KindExtRoute),
			AdminNetworkPolicy:         can(model.KindANP),
			BaselineAdminNetworkPolicy: can(model.KindBANP),
		}
		out.Caps.Platform, _ = s.canAuthorPlatform(ctx, id, c)
	}
	// Which file declares each segment: the edit/delete affordance. A shared
	// segment's platform file shows only to callers who may author that tier.
	declared := s.sourceFiles(r.Context(), id, c, out.CanManage)
	for i := range out.Networks {
		n := &out.Networks[i]
		n.SourceFile = declared(n.Backing, n.Namespace, n.Name)
	}
	// The physical fabric is node-level infrastructure - show it only to callers
	// who can read nodes (cluster-admins), not every tenant who can attach a NIC.
	if s.capable(r.Context(), id, c, ask(verbList, ssarNodes, "")) {
		out.Uplinks = full.Uplinks
		out.PhysicalAdapters = full.PhysicalAdapters
		if out.Caps.Uplink {
			for i := range out.Uplinks {
				u := &out.Uplinks[i]
				u.SourceFile = declared(model.KindNNCP.Kind, "", u.Policy)
				if d, ok := s.driftFor(model.KindNNCP.Kind, "", u.Policy); ok {
					u.Sync, u.Health, u.SyncError = d.Sync, d.Health, d.Message
				}
			}
		}
	}
	writeJSON(w, out)
}

// scopeNetworks keeps shared (cluster) networks and only the project networks in
// namespaces the caller can see. A shared network publishes cluster-wide, so its
// own Namespaces list can name other tenants' namespaces - filter it to the
// caller's visible set so the port group stays discoverable (name/kind/VLAN, like
// a StorageClass) without ever revealing a namespace outside the caller's RBAC.
func scopeNetworks(nets []model.Network, visible map[string]bool) []model.Network {
	out := make([]model.Network, 0, len(nets))
	for _, n := range nets {
		switch {
		case n.Scope == model.ScopeShared:
			n.Namespaces = visibleSubset(n.Namespaces, visible)
			out = append(out, n)
		case visible[n.Namespace]:
			out = append(out, n)
		}
	}
	return out
}

// redactSharedErrors replaces a shared network's apply error with a generic
// line: a CUDN's error can name every namespace its selector reaches, which
// only its authors may read.
func redactSharedErrors(nets []model.Network) {
	for i := range nets {
		if nets[i].Scope == model.ScopeShared && nets[i].SyncError != "" {
			nets[i].SyncError = sharedNetworkErrorForTenants
		}
	}
}

const sharedNetworkErrorForTenants = "the platform could not apply this network"

// enrichNetworkDrift attaches each segment's own ArgoCD sync/health/apply-error
// - the same per-object drift plane VMs use. Mutates the scoped copies in place
// (never the cached catalog, which scopeNetworks already copied).
func (s *Server) enrichNetworkDrift(nets []model.Network) {
	for i := range nets {
		// UDN/NAD are namespaced; a CUDN is cluster-scoped, so its resource namespace is
		// empty (Network.Namespace is already "" for shared networks).
		if d, ok := s.driftFor(nets[i].Backing, nets[i].Namespace, nets[i].Name); ok {
			nets[i].Sync, nets[i].Health, nets[i].SyncError = d.Sync, d.Health, d.Message
		}
	}
}

// visibleSubset returns the namespaces in nss the caller can see, as a fresh slice
// (never aliasing the cached catalog's backing array, which scopeNetworks must not
// mutate). nil when none remain.
func visibleSubset(nss []string, visible map[string]bool) []string {
	var out []string
	for _, ns := range nss {
		if visible[ns] {
			out = append(out, ns)
		}
	}
	return out
}
