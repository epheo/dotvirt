package api

import (
	"encoding/json"
	"net/http"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
)

// The networking create routes resolve their target TIER from the object's SCOPE,
// never from a client-supplied repo: a namespace-scoped UDN goes to the tenant's own
// project repo; cluster-scoped objects (CUDN, NNCP uplink, Namespace) go to the
// platform repo and are SSAR-gated on the caller's authority to create that kind —
// matching the AppProject boundary that lets only the platform app apply them.

// handleCreateNetwork stages a new Distributed Port Group, proposed as a PR (the
// same owns-nothing path as a VM create). A "project"-scoped Layer2 secondary UDN
// lands in the tenant repo owning its namespace; a shared/VLAN CUDN is
// cluster-scoped, so it routes to the platform tier.
func (s *Server) handleCreateNetwork(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var peek struct {
		Scope     string `json:"scope"`
		Namespace string `json:"namespace"`
	}
	_ = json.Unmarshal(raw, &peek)

	var sc scope
	var ok bool
	switch peek.Scope {
	case "", netgen.ScopeProject:
		if peek.Namespace == "" {
			http.Error(w, "a namespace is required for a project-scoped network", http.StatusBadRequest)
			return
		}
		sc, ok = s.resolveProject(w, r, byNamespace(peek.Namespace))
	default: // ScopeShared / ScopeVLAN — a cluster-scoped CUDN
		sc, ok = s.platformScope(w, r, "k8s.ovn.org", "clusteruserdefinednetworks")
	}
	if !ok {
		return
	}
	view, err := s.draft.StageCreateNetwork(sc.id, sc.proj, raw)
	respond(w, view, err)
}

// handleCreateUplink stages a new Uplink (an nmstate NNCP) — always cluster-scoped,
// so always the platform tier, gated on the caller's authority to create NNCPs.
func (s *Server) handleCreateUplink(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sc, ok := s.platformScope(w, r, "nmstate.io", "nodenetworkconfigurationpolicies")
	if !ok {
		return
	}
	view, err := s.draft.StageCreateUplink(sc.id, sc.proj, raw)
	respond(w, view, err)
}

// handleCreateNamespace stages a new namespace (+ optional primary "VM Network").
// The Namespace object is cluster-scoped, so it is COMMITTED to the platform repo
// and gated on namespace-create authority; but it is labeled/annotated to the tenant
// project it JOINS (carried as "project"), so that project's Argo app syncs
// workloads into it once the platform app creates it.
func (s *Server) handleCreateNamespace(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var peek struct {
		Project string `json:"project"`
	}
	_ = json.Unmarshal(raw, &peek)
	if peek.Project == "" {
		http.Error(w, "the project the namespace joins is required", http.StatusBadRequest)
		return
	}
	// The tenant project it joins: annotation source + authz (the caller must see it).
	join, ok := s.resolveProject(w, r, byName(peek.Project))
	if !ok {
		return
	}
	// Commit to the platform tier, gated on namespace-create authority.
	plat, ok := s.platformScope(w, r, "", "namespaces")
	if !ok {
		return
	}
	view, err := s.draft.StageCreateNamespace(join.id, plat.proj, join.proj, raw)
	respond(w, view, err)
}

// handleNetworks lists the networks (Distributed Port Groups) the caller may
// attach a VM to, plus the physical fabric (Uplinks + Physical adapters) for
// callers who can read nodes. The port-group catalog is read with dotvirt's SA —
// a scoped tenant usually can't list these cluster CRDs, which would yield a
// silently-empty picker — then project-scoped networks are filtered to the
// caller's visible namespaces so nothing leaks across tenants.
func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// The full SA catalog is identical for everyone; cache it to skip the cluster
	// LISTs per request. Per-tenant scoping happens below, off the cached copy.
	full, ok := s.networks.Get("all")
	if !ok {
		sa, err := s.clusterF.SA()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		full, err = sa.NetworkCatalog(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.networks.Put("all", full)
	}

	visible, err := s.visibleFor(r.Context(), id, c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := model.NetworkInventory{
		Networks:         scopeNetworks(full.Networks, visible),
		Uplinks:          []model.Uplink{},
		PhysicalAdapters: []model.PhysicalAdapter{},
		NMStatePresent:   full.NMStatePresent,
	}
	// The physical fabric is node-level infrastructure — show it only to callers
	// who can read nodes (cluster-admins), not every tenant who can attach a NIC.
	if c.CanReadNodes(r.Context()) {
		out.Uplinks = full.Uplinks
		out.PhysicalAdapters = full.PhysicalAdapters
	}
	// Authoring signal for the UI: a platform repo must be configured and the caller
	// must be able to create cluster-scoped networks (the platform-operator signal,
	// also satisfied by cluster-admins). Gates the New VLAN / Add Uplink / New
	// Namespace actions, matching the platformScope gate the create routes enforce.
	out.CanManage = s.cfg.PlatformRepo != "" && c.CanCreateClusterResource(r.Context(), "k8s.ovn.org", "clusteruserdefinednetworks")
	writeJSON(w, http.StatusOK, out)
}

// scopeNetworks keeps shared (cluster) networks and only the project networks in
// namespaces the caller can see. A shared network publishes cluster-wide, so its
// own Namespaces list can name other tenants' namespaces — filter it to the
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
