package api

import (
	"context"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// The effective-policy answer: what governs one workload, in evaluation order.
// Served entirely from the SA snapshots (netstate policies + clusterstate
// labels) — no per-request cluster read; resolveProject gates the namespace
// exactly like the other detail views.

// handleVMPolicy answers for one VM, matching pod selectors against the labels
// its virt-launcher pod carries (live VMI, else the manifest template's).
func (s *Server) handleVMPolicy(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	lbls, live, found := s.state.WorkloadLabels(ns, name)
	if !found {
		http.Error(w, "vm not found", http.StatusNotFound)
		return
	}
	eff := s.effectivePolicy(ns, lbls, true)
	eff.VM, eff.Labels, eff.LabelsLive = name, lbls, live
	s.redactEffective(r.Context(), sc, &eff)
	writeJSON(w, http.StatusOK, eff)
}

// handleNamespacePolicy answers for a whole namespace: pod-selecting policies
// come back conditional rather than resolved.
func (s *Server) handleNamespacePolicy(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	sc, ok := s.resolveProject(w, r, byNamespace(ns))
	if !ok {
		return
	}
	eff := s.effectivePolicy(ns, nil, false)
	s.redactEffective(r.Context(), sc, &eff)
	writeJSON(w, http.StatusOK, eff)
}

// clusterAuthority reports per kind whether the caller may read the
// cluster-tier rollup — the same create-SSAR /api/policies and the create
// routes enforce, cached per (token, kind).
func (s *Server) clusterAuthority(ctx context.Context, id auth.Identity, c *cluster.Client) func(model.PolicyKind) bool {
	return func(k model.PolicyKind) bool {
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
}

// redactSubjects strips a cluster-tier policy's enumerated subject namespaces
// for callers without authority over the kind. The rules stay — a rule that
// governs the caller's workload is never hidden — but where else an admin
// policy applies (namespaces across other tenants) is the rollup-tier
// information /api/policies gates, so the same authority gates it here.
func redactSubjects(can func(model.PolicyKind) bool, p *model.Policy) {
	switch p.Kind {
	case model.PolicyAdmin, model.PolicyBaseline, model.PolicyEgressIP, model.PolicyRoute:
		if p.Namespaces != nil && !can(p.Kind) {
			p.Namespaces = nil
		}
	}
}

func (s *Server) redactEffective(ctx context.Context, sc scope, eff *model.EffectivePolicy) {
	can := s.clusterAuthority(ctx, sc.id, sc.cluster)
	for _, bs := range [][]model.PolicyBinding{eff.EastWest, eff.Gateway, eff.SNAT, eff.Routes} {
		for i := range bs {
			redactSubjects(can, &bs[i].Policy)
		}
	}
}

func (s *Server) effectivePolicy(ns string, podLabels map[string]string, podScoped bool) model.EffectivePolicy {
	if s.netstate == nil {
		return model.EffectivePolicy{Namespace: ns}
	}
	var nsLabels map[string]string
	for _, n := range s.state.Namespaces() {
		if n.Name == ns {
			nsLabels = n.Labels
			break
		}
	}
	eff := s.netstate.Effective(ns, nsLabels, podLabels, podScoped)
	for _, bs := range [][]model.PolicyBinding{eff.EastWest, eff.Gateway, eff.SNAT, eff.Routes} {
		s.enrichBindingDrift(bs)
	}
	return eff
}

func (s *Server) enrichBindingDrift(bs []model.PolicyBinding) {
	if s.drift == nil {
		return
	}
	for i := range bs {
		s.policyDrift(&bs[i].Policy)
	}
}
