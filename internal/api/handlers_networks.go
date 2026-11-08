package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/netstate"
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

// stageCreate is every network-family create's tail, past its gates: stage
// the form, or render it for a dry run.
func (s *Server) stageCreate(w http.ResponseWriter, r *http.Request, sc scope, resource draft.Resource, raw []byte) {
	if isDryRun(r) {
		p, err := s.draft.PreviewCreate(sc.id, sc.proj, resource, raw)
		respond(w, p, err)
		return
	}
	view, err := s.draft.StageCreate(sc.id, sc.proj, resource, raw)
	respond(w, view, err)
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
		s.stageCreate(w, r, sc, resource, raw)
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
		s.stageCreate(w, r, sc, resource, raw)
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
	s.stageCreate(w, r, sc, draft.ResourceNetwork, raw)
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
	// The priority check runs before the dry-run branch, so a dry run refuses
	// a held priority exactly as the stage does.
	if !p.Baseline && s.netstate != nil {
		if other, taken := s.netstate.ANPAtPriority(p.Priority, p.Name); taken {
			fail(w, fmt.Errorf("%w: priority %d is held by admin network policy %q; pick a free one, equal priorities apply in an undefined order",
				model.ErrConflict, p.Priority, other))
			return
		}
	}
	s.stageCreate(w, r, sc, draft.ResourceAdminNetworkPolicy, raw)
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

	ctx := r.Context()
	can := func(k model.Kind) bool { return s.capable(ctx, id, c, kindAction(verbCreate, k, "")) }
	cudn := can(model.KindCUDN)
	platformRead, _ := s.canReadPlatform(ctx, id, c)
	out := model.NetworkInventory{
		Networks:         scopeNetworks(full.Networks, visible, cudn || platformRead),
		Uplinks:          []model.Uplink{},
		PhysicalAdapters: []model.PhysicalAdapter{},
		NMStatePresent:   full.NMStatePresent,
	}
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
		out.Caps.Platform = platformRead
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

// scopeNetworks is the catalog one caller sees: project networks in the
// namespaces they can see, and shared networks published to at least one of
// them, its Namespaces narrowed to those (a shared network's name and its
// other namespaces echo other tenants). A caller with platform networking
// authority (all) sees every shared network, published or not, to fix it.
func scopeNetworks(nets []model.Network, visible map[string]bool, all bool) []model.Network {
	out := make([]model.Network, 0, len(nets))
	for _, n := range nets {
		switch {
		case n.Scope == model.ScopeShared:
			n.Namespaces = visibleSubset(n.Namespaces, visible)
			if all || len(n.Namespaces) > 0 {
				out = append(out, n)
			}
		case visible[n.Namespace]:
			out = append(out, n)
		}
	}
	return out
}

// callerNetworks is the catalog as /api/networks serves it to this caller, for
// the routes that resolve one network by key: a network the caller cannot
// see there is not found here either.
func (s *Server) callerNetworks(ctx context.Context, id auth.Identity, c *cluster.Client, visible map[string]bool) []model.Network {
	if s.netstate == nil {
		return nil
	}
	all := s.capable(ctx, id, c, kindAction(verbCreate, model.KindCUDN, ""))
	if !all {
		all, _ = s.canReadPlatform(ctx, id, c)
	}
	return scopeNetworks(s.netstate.Catalog().Networks, visible, all)
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

// handleNetworkRules answers what bears on one network's VMs: the rules
// binding its namespaces and the rules elsewhere naming those VMs as their
// other end, from the SA policy snapshot. The network resolves through the
// caller-scoped catalog, so a network in a namespace the caller can't see is
// not found, and a shared network spans only its visible namespaces.
func (s *Server) handleNetworkRules(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	if s.netstate == nil {
		fail(w, fmt.Errorf("%w: networking is not wired", model.ErrUnavailable))
		return
	}
	visible, err := s.visibleFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return
	}
	key := r.URL.Query().Get("network")
	n, ok := findNetwork(s.callerNetworks(r.Context(), id, c, visible), key)
	if !ok {
		fail(w, fmt.Errorf("%w: network %q", model.ErrNotFound, key))
		return
	}
	nss := n.Namespaces
	if n.Scope == model.ScopeProject {
		nss = []string{n.Namespace}
	}
	pr := s.policyRedactor(r.Context(), id, c)
	// A peer rule's sentence names its subject; a caller without authority
	// over the cluster tier reads it with other tenants' names hidden.
	var red *netstate.Redaction
	if !pr.can(model.PolicyAdmin) || !pr.can(model.PolicyBaseline) {
		red = pr.view
	}
	out := s.netstate.NetworkRules(nss, s.state.NamespaceLabels, red, s.cfg.ProjectLabel)
	out.Network = key
	out = scopeNetworkRules(out, visible, pr.apply)
	for i := range out.Applies {
		s.policyDrift(&out.Applies[i].Policy)
	}
	for i := range out.Reaches {
		s.policyDrift(&out.Reaches[i].Policy)
	}
	writeJSON(w, out)
}

// findNetwork resolves a network key the way the UI routes it: a project
// network by its "namespace/name" attach ref, a shared one by name; a bare
// name falls back to the first network so named.
func findNetwork(nets []model.Network, key string) (model.Network, bool) {
	for _, n := range nets {
		if n.AttachRef == key || (n.Scope == model.ScopeShared && n.Name == key) {
			return n, true
		}
	}
	for _, n := range nets {
		if n.Name == key {
			return n, true
		}
	}
	return model.Network{}, false
}

// scopeNetworkRules applies the policy plane's visibility to one network's
// rules. A binding governs the caller's own VMs, so it stays, redacted like
// the effective view. A peer rule in another namespace shows only where the
// caller sees that namespace; a cluster rule naming these VMs governs them
// too, so it stays, redacted.
func scopeNetworkRules(in model.NetworkRules, visible map[string]bool, redact func(*model.Policy, *model.PolicyRuleView)) model.NetworkRules {
	for i := range in.Applies {
		redact(&in.Applies[i].Policy, nil)
	}
	reaches := make([]model.PeerRule, 0, len(in.Reaches))
	for _, pr := range in.Reaches {
		if pr.Policy.Namespace != "" && !visible[pr.Policy.Namespace] {
			continue
		}
		redact(&pr.Policy, &pr.Rule)
		reaches = append(reaches, pr)
	}
	in.Reaches = reaches
	return in
}
