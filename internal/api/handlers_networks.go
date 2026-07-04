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

// handleCreateAdminNetworkPolicy stages a cluster-wide admin DFW policy
// (AdminNetworkPolicy or the baseline default) — always platform-tier and admin-only,
// gated on the caller's authority to create the matching kind.
func (s *Server) handleCreateAdminNetworkPolicy(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var peek struct {
		Baseline bool `json:"baseline"`
	}
	_ = json.Unmarshal(raw, &peek)
	resource := "adminnetworkpolicies"
	if peek.Baseline {
		resource = "baselineadminnetworkpolicies"
	}
	sc, ok := s.platformScope(w, r, "policy.networking.k8s.io", resource)
	if !ok {
		return
	}
	view, err := s.draft.StageCreateAdminNetworkPolicy(sc.id, sc.proj, raw)
	respond(w, view, err)
}

// handleCreateNetworkPolicy stages a NetworkPolicy (the east-west Distributed
// Firewall) — namespace-scoped, so it routes to the tenant project owning the
// namespace; the tenant's Argo app applies it on merge.
func (s *Server) handleCreateNetworkPolicy(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var peek struct {
		Namespace string `json:"namespace"`
	}
	_ = json.Unmarshal(raw, &peek)
	if peek.Namespace == "" {
		http.Error(w, "a namespace is required for a network policy", http.StatusBadRequest)
		return
	}
	sc, ok := s.resolveProject(w, r, byNamespace(peek.Namespace))
	if !ok {
		return
	}
	view, err := s.draft.StageCreateNetworkPolicy(sc.id, sc.proj, raw)
	respond(w, view, err)
}

// handleCreateEgressIP stages a cluster-scoped EgressIP (the Tier-0 source-NAT pool)
// — always platform-tier, gated on the caller's authority to create EgressIPs.
func (s *Server) handleCreateEgressIP(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sc, ok := s.platformScope(w, r, "k8s.ovn.org", "egressips")
	if !ok {
		return
	}
	view, err := s.draft.StageCreateEgressIP(sc.id, sc.proj, raw)
	respond(w, view, err)
}

// handleCreateExternalRoute stages a cluster-scoped AdminPolicyBasedExternalRoute (the
// Tier-0 external next-hop route) — always platform-tier, gated on the caller's
// authority to create them.
func (s *Server) handleCreateExternalRoute(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sc, ok := s.platformScope(w, r, "k8s.ovn.org", "adminpolicybasedexternalroutes")
	if !ok {
		return
	}
	view, err := s.draft.StageCreateExternalRoute(sc.id, sc.proj, raw)
	respond(w, view, err)
}

// handleCreateEgressFirewall stages a namespace's egress firewall (the Tier-1
// gateway firewall) — namespace-scoped, so it routes to the tenant project owning
// the namespace, the same path as a project-scoped UDN. The tenant's Argo app applies
// it on merge (its AppProject must permit k8s.ovn.org/EgressFirewall).
func (s *Server) handleCreateEgressFirewall(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var peek struct {
		Namespace string `json:"namespace"`
	}
	_ = json.Unmarshal(raw, &peek)
	if peek.Namespace == "" {
		http.Error(w, "a namespace is required for an egress firewall", http.StatusBadRequest)
		return
	}
	sc, ok := s.resolveProject(w, r, byNamespace(peek.Namespace))
	if !ok {
		return
	}
	view, err := s.draft.StageCreateEgressFirewall(sc.id, sc.proj, raw)
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

// handleCreateProject bootstraps a new tenant project from the UI — the "New
// Project" flow. It creates the project's forge repo and stages its first namespace
// (+ an optional owners RoleBinding) into the platform repo. Gated on the same
// namespace-create authority as handleCreateNamespace: creating a tenant is a
// platform-admin act (it lands a Namespace + RBAC in the platform tier).
func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Peek the name for an early 400; the Coordinator re-decodes the full spec.
	var peek struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if peek.Name == "" {
		http.Error(w, "a project name is required", http.StatusBadRequest)
		return
	}
	plat, ok := s.platformScope(w, r, "", "namespaces")
	if !ok {
		return
	}
	view, err := s.draft.StageCreateProject(plat.id, plat.proj, raw)
	respond(w, view, err)
}

// handleAdoptProject wires a repo to an existing labeled-but-repoless project — the
// "Attach repo" action on the inventory's no-repo dead-end. Like handleCreateProject
// it's a platform-admin act (it lands a Namespace + repo annotation in the platform
// tier), so it's gated on namespace-create authority; the target tenant is resolved
// from the SA snapshot (the caller is a platform admin) and must currently be repoless.
func (s *Server) handleAdoptProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Owners []string `json:"owners,omitempty"`
	}
	if raw, err := readAll(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	} else if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	plat, ok := s.platformScope(w, r, "", "namespaces")
	if !ok {
		return
	}
	target, ok := s.projectByName(r.PathValue("project"))
	if !ok {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	view, err := s.draft.AdoptProject(plat.id, plat.proj, target, body.Owners)
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

	// The full catalog is a lock-free scan of the SA-maintained netstate snapshot
	// (watch-fed, identical for everyone) — no per-request cluster LIST. Per-tenant
	// scoping and per-object drift happen below, off this copy.
	full := s.netstate.Catalog()

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
	// Per-object drift: attach each segment's own ArgoCD sync/health at serve time
	// (always fresh, off the cached catalog) — the same surface VMs carry.
	s.enrichNetworkDrift(out.Networks)
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
	// Per-action authoring authority: the same SSARs the create handlers enforce, so
	// the UI gates each button precisely. CanManage stays the coarse CUDN signal that
	// gates the platform-draft view. All false when no platform repo is configured.
	if s.cfg.PlatformRepo != "" {
		ctx := r.Context()
		cudn := c.CanCreateClusterResource(ctx, "k8s.ovn.org", "clusteruserdefinednetworks")
		out.CanManage = cudn
		out.Caps = model.NetworkCaps{
			SharedSegment:      cudn,
			Uplink:             c.CanCreateClusterResource(ctx, "nmstate.io", "nodenetworkconfigurationpolicies"),
			Namespace:          c.CanCreateClusterResource(ctx, "", "namespaces"),
			EgressIP:           c.CanCreateClusterResource(ctx, "k8s.ovn.org", "egressips"),
			ExternalRoute:      c.CanCreateClusterResource(ctx, "k8s.ovn.org", "adminpolicybasedexternalroutes"),
			AdminNetworkPolicy: c.CanCreateClusterResource(ctx, "policy.networking.k8s.io", "adminnetworkpolicies"),
		}
	}
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

// enrichNetworkDrift attaches each segment's own ArgoCD sync/health/apply-error,
// looked up from the shared Application snapshot by object identity — the same
// per-object drift plane VMs use. Mutates the scoped copies in place (never the cached
// catalog, which scopeNetworks already copied). No-op when Argo isn't wired.
func (s *Server) enrichNetworkDrift(nets []model.Network) {
	if s.drift == nil {
		return
	}
	for i := range nets {
		group, kind := networkGVK(nets[i].Backing)
		if kind == "" {
			continue
		}
		// UDN/NAD are namespaced; a CUDN is cluster-scoped, so its resource namespace is
		// empty (Network.Namespace is already "" for shared networks).
		if d, ok := s.drift.ResourceDrift(group, kind, nets[i].Namespace, nets[i].Name); ok {
			nets[i].Sync, nets[i].Health, nets[i].SyncError = d.Sync, d.Health, d.Message
		}
	}
}

// networkGVK maps a segment's backing to its ArgoCD (group, kind); empty kind means a
// backing with no managed object to drift-check.
func networkGVK(backing string) (group, kind string) {
	switch backing {
	case "UserDefinedNetwork":
		return "k8s.ovn.org", "UserDefinedNetwork"
	case "ClusterUserDefinedNetwork":
		return "k8s.ovn.org", "ClusterUserDefinedNetwork"
	case "NetworkAttachmentDefinition":
		return "k8s.cni.cncf.io", "NetworkAttachmentDefinition"
	}
	return "", ""
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
