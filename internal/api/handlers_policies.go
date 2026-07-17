package api

import (
	"net/http"

	"github.com/epheo/dotvirt/internal/model"
)

// handlePolicies lists the policy plane (the Security view): namespace-tier rows
// (NetworkPolicy, EgressFirewall) scoped to the caller's visible namespaces, and
// cluster-tier rows gated per kind on the same create-SSAR the matching modal and
// create route enforce — an admin policy's rules name namespaces across tenants,
// so only a caller with authority over that kind may read the rollup. The catalog
// is the SA-maintained netstate snapshot; per-object drift attaches at serve time.
func (s *Server) handlePolicies(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return
	}
	var all []model.Policy
	if s.netstate != nil {
		all = s.netstate.Policies()
	}
	visible, err := s.visibleFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return
	}
	// Visibility is authority over the kind, not platform authoring: unlike the
	// caps in /api/networks it doesn't require a platform repo — a cluster-admin
	// without one still audits the live policy plane.
	ctx := r.Context()
	canCluster := func(k model.PolicyKind) bool {
		switch k {
		case model.PolicyAdmin:
			return s.canCreateCached(ctx, id, c, ssarANP)
		case model.PolicyBaseline:
			return s.canCreateCached(ctx, id, c, ssarBANP)
		case model.PolicyEgressIP:
			return s.canCreateCached(ctx, id, c, ssarEgressIP)
		case model.PolicyRoute:
			return s.canCreateCached(ctx, id, c, ssarExtRoute)
		}
		return false
	}
	out := scopePolicies(all, visible, canCluster)
	s.enrichPolicyDrift(out)
	writeJSON(w, http.StatusOK, model.PolicyInventory{Policies: out})
}

// scopePolicies keeps namespace-tier policies in visible namespaces and
// cluster-tier policies the caller has authority over. Returns fresh slices only
// by construction — the netstate catalog built each row per call, so mutating the
// kept rows (drift enrichment) is safe.
func scopePolicies(all []model.Policy, visible map[string]bool, canCluster func(model.PolicyKind) bool) []model.Policy {
	out := make([]model.Policy, 0, len(all))
	for _, p := range all {
		switch {
		case p.Namespace != "":
			if visible[p.Namespace] {
				out = append(out, p)
			}
		case canCluster(p.Kind):
			out = append(out, p)
		}
	}
	return out
}

// enrichPolicyDrift attaches each policy's own ArgoCD sync/health from the shared
// Application snapshot — the same per-object drift plane VMs and networks use.
func (s *Server) enrichPolicyDrift(pols []model.Policy) {
	if s.drift == nil {
		return
	}
	for i := range pols {
		group, kind := policyGVK(pols[i].Backing)
		if kind == "" {
			continue
		}
		if d, ok := s.drift.ResourceDrift(group, kind, pols[i].Namespace, pols[i].Name); ok {
			pols[i].Sync, pols[i].Health, pols[i].SyncError = d.Sync, d.Health, d.Message
		}
	}
}

// policyGVK maps a policy's backing to its ArgoCD (group, kind).
func policyGVK(backing string) (group, kind string) {
	switch backing {
	case "NetworkPolicy":
		return "networking.k8s.io", backing
	case "AdminNetworkPolicy", "BaselineAdminNetworkPolicy":
		return "policy.networking.k8s.io", backing
	case "EgressFirewall", "EgressIP", "AdminPolicyBasedExternalRoute":
		return "k8s.ovn.org", backing
	}
	return "", ""
}
