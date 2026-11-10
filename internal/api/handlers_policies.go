package api

import (
	"net/http"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/model"
)

// handlePolicies lists the policy plane (the Security view): namespace-tier rows
// where the caller may list their kind (scopePolicies), and cluster-tier rows
// of each kind the caller may list cluster-wide, as kubectl shows them. The
// catalog is the SA-maintained netstate snapshot; per-object drift attaches at
// serve time.
func (s *Server) handlePolicies(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	var all []model.Policy
	if s.netstate != nil {
		all = s.netstate.Policies()
	}
	v, err := s.viewFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return
	}
	// Reading needs no platform repo: a cluster reader without one still audits
	// the live policy plane.
	out := scopePolicies(all, v, s.clusterAuthority(r.Context(), id, c, verbList))
	s.enrichPolicyDrift(out)
	// Which file declares each policy: the edit/delete affordance.
	declared := s.sourceFiles(r.Context(), id, c, true)
	for i := range out {
		p := &out[i]
		p.SourceFile = declared(p.Backing, p.Namespace, p.Name)
	}
	writeJSON(w, model.PolicyInventory{Policies: out})
}

// scopePolicies keeps namespace-tier policies the caller may list in the
// project namespaces they live in and cluster-tier policies of each kind it
// lists. The kept rows are
// copies, so drift enrichment may set their fields; the slices inside them
// are the snapshot's, never written.
func scopePolicies(all []model.Policy, v callerView, listsCluster func(model.PolicyKind) bool) []model.Policy {
	out := make([]model.Policy, 0, len(all))
	for _, p := range all {
		switch {
		case p.Namespace != "":
			if v.showsInProject(p.Backing, p.Namespace) {
				out = append(out, p)
			}
		case listsCluster(p.Kind):
			out = append(out, p)
		}
	}
	return out
}

// driftFor is one managed object's ArgoCD sync/health from the shared
// Application snapshot, addressed by its Backing (also its kind). ok=false when
// Argo isn't wired, the backing is unmanaged, or no Application manages it.
func (s *Server) driftFor(backing, namespace, name string) (argo.Drift, bool) {
	_, kind, ok := model.LookupKind(backing)
	if s.drift == nil || !ok {
		return argo.Drift{}, false
	}
	return s.drift.ResourceDrift(kind.Group, backing, namespace, name)
}

// enrichPolicyDrift attaches each policy's own ArgoCD sync/health - the same
// per-object drift plane VMs and networks use.
func (s *Server) enrichPolicyDrift(pols []model.Policy) {
	for i := range pols {
		s.policyDrift(&pols[i])
	}
}

func (s *Server) policyDrift(p *model.Policy) {
	if d, ok := s.driftFor(p.Backing, p.Namespace, p.Name); ok {
		p.Sync, p.Health, p.SyncError = d.Sync, d.Health, d.Message
	}
}
