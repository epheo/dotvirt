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

// handleCreateNetwork stages a new network, proposed as a PR (the
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

// handleCreateAdminNetworkPolicy stages a cluster rule
// (AdminNetworkPolicy or the baseline default) - always platform-tier and admin-only,
// gated like platformCreate on the matching kind. A priority a live policy
// already holds is refused: OVN-K would order the two arbitrarily. Only a
// caller who may list the kind reads that policy's name, or may re-stage a
// policy at the priority it holds itself.
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
		// A caller who may not list cluster rules names none: whatever name
		// they send, a held priority answers the same.
		lists := s.clusterAuthority(r.Context(), sc.id, sc.cluster, verbList)(model.PolicyAdmin)
		self, holder := p.Name, "another cluster rule"
		if !lists {
			self = ""
		}
		if other, taken := s.netstate.ANPAtPriority(p.Priority, self); taken {
			if lists {
				holder = fmt.Sprintf("admin network policy %q", other)
			}
			fail(w, fmt.Errorf("%w: priority %d is held by %s; pick a free one, equal priorities apply in an undefined order",
				model.ErrConflict, p.Priority, holder))
			return
		}
	}
	s.stageCreate(w, r, sc, draft.ResourceAdminNetworkPolicy, raw)
}

// handleNetworks lists the networks the caller may list (scopeNetworks), plus
// each half of the physical fabric (uplinks, physical adapters) to a caller
// who may list its kind. The rows come from dotvirt's SA-fed netstate
// snapshot, so they are narrowed per caller here.
func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}

	// The full catalog is an in-memory read of the SA-maintained netstate snapshot
	// (watch-fed, identical for everyone) - no per-request cluster LIST. Per-tenant
	// scoping and per-object drift happen below, off this copy. Nil netstate (a
	// stripped-down wiring) degrades to an empty catalog, per the Deps contract.
	var full model.NetworkInventory
	if s.netstate != nil {
		full = s.netstate.Catalog()
	}

	v, err := s.viewFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return
	}

	ctx := r.Context()
	can := func(k model.Kind) bool { return s.capable(ctx, id, c, kindAction(verbCreate, k, "")) }
	cudn := can(model.KindCUDN)
	out := model.NetworkInventory{
		Networks:         scopeNetworks(full.Networks, v, s.seesAllNetworks(ctx, id, c)),
		Uplinks:          []model.Uplink{},
		PhysicalAdapters: []model.PhysicalAdapter{},
		NMStatePresent:   full.NMStatePresent,
	}
	// Per-object drift: attach each network's own ArgoCD sync/health at serve time
	// (always fresh, off the cached catalog) - the same surface VMs carry.
	s.enrichNetworkDrift(out.Networks)
	if !cudn {
		redactSharedErrors(out.Networks)
	}
	// Which file declares each network: the edit/delete affordance. A shared
	// network's platform file shows only to callers who may author that tier.
	authors := s.cfg.PlatformRepo != ""
	declared := s.sourceFiles(r.Context(), id, c, authors && cudn)
	for i := range out.Networks {
		n := &out.Networks[i]
		n.SourceFile = declared(n.Backing, n.Namespace, n.Name)
	}
	// The physical fabric: each half follows its own kind.
	if s.capable(r.Context(), id, c, ask(verbList, ssarNNS, "")) {
		out.PhysicalAdapters = full.PhysicalAdapters
	}
	if s.capable(r.Context(), id, c, ask(verbList, ssarUplink, "")) {
		out.Uplinks = full.Uplinks
		if authors && can(model.KindNNCP) {
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
// project namespaces they can see, and shared networks published to at least
// one of them, its Namespaces narrowed to those (a shared network's name and
// its other namespaces echo other tenants). A caller with platform networking
// authority (all) sees every shared network, published or not, to fix it.
func scopeNetworks(nets []model.Network, v callerView, all bool) []model.Network {
	out := make([]model.Network, 0, len(nets))
	for _, n := range nets {
		switch {
		case n.Scope == model.ScopeShared:
			// What a tenant reads of one is its NetworkAttachmentDefinition. A
			// fresh slice: the cached catalog's is never written.
			var published []string
			for _, ns := range n.Namespaces {
				if v.showsInProject(model.KindNAD.Kind, ns) {
					published = append(published, ns)
				}
			}
			n.Namespaces = published
			if all || len(n.Namespaces) > 0 {
				out = append(out, n)
			}
		case v.showsInProject(n.Backing, n.Namespace):
			out = append(out, n)
		}
	}
	return out
}

// callerNetworks is the catalog as /api/networks serves it to this caller, for
// the routes that resolve one network by key: a network the caller cannot
// see there is not found here either.
func (s *Server) callerNetworks(ctx context.Context, id auth.Identity, c *cluster.Client, v callerView) []model.Network {
	if s.netstate == nil {
		return nil
	}
	return scopeNetworks(s.netstate.Catalog().Networks, v, s.seesAllNetworks(ctx, id, c))
}

// seesAllNetworks reports whether the caller's catalog holds every shared
// network, published to them or not: they may list shared networks
// cluster-wide, as kubectl answers it.
func (s *Server) seesAllNetworks(ctx context.Context, id auth.Identity, c *cluster.Client) bool {
	return s.capable(ctx, id, c, kindAction(verbList, model.KindCUDN, ""))
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

// enrichNetworkDrift attaches each network's own ArgoCD sync/health/apply-error
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
	v, err := s.viewFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return
	}
	key := r.URL.Query().Get("network")
	n, ok := findNetwork(s.callerNetworks(r.Context(), id, c, v), key)
	if !ok {
		fail(w, fmt.Errorf("%w: network %q", model.ErrNotFound, key))
		return
	}
	nss := n.Namespaces
	if n.Scope == model.ScopeProject {
		nss = []string{n.Namespace}
	}
	pr := s.policyRedactor(r.Context(), id, c)
	// A peer rule's sentence names its subject; a caller who may not list the
	// admin policy kinds reads it with other tenants' names hidden.
	var red *netstate.Redaction
	if !pr.cluster(model.PolicyAdmin) || !pr.cluster(model.PolicyBaseline) {
		red = pr.view
	}
	out := s.netstate.NetworkRules(nss, s.state.NamespaceLabels, red, s.cfg.ProjectLabel)
	out.Network = key
	writeJSON(w, scopeNetworkRules(out, v, s.policyDrift, pr.apply))
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
// policy plane lists it, a project namespace the caller sees; a cluster rule
// naming these VMs governs them too, so it stays, redacted. drift attaches each kept policy's apply state
// first, so the redactor sees, and hides, a cluster rule's apply error.
func scopeNetworkRules(in model.NetworkRules, v callerView, drift func(*model.Policy), redact func(*model.Policy, *model.PolicyRuleView)) model.NetworkRules {
	for i := range in.Applies {
		drift(&in.Applies[i].Policy)
		redact(&in.Applies[i].Policy, nil)
	}
	reaches := make([]model.PeerRule, 0, len(in.Reaches))
	for _, pr := range in.Reaches {
		if ns := pr.Policy.Namespace; ns != "" && !v.showsInProject(pr.Policy.Backing, ns) {
			continue
		}
		drift(&pr.Policy)
		redact(&pr.Policy, &pr.Rule)
		reaches = append(reaches, pr)
	}
	in.Reaches = reaches
	return in
}
