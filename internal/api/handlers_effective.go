package api

import (
	"net/http"

	"github.com/epheo/dotvirt/internal/model"
)

// The effective-policy answer: what governs one workload, in evaluation order.
// Served entirely from the SA snapshots (netstate policies + clusterstate
// labels) — no per-request cluster read; resolveProject gates the namespace
// exactly like the other detail views.

// handleVMPolicy answers for one VM, matching pod selectors against the labels
// its virt-launcher pod carries (live VMI, else the manifest template's).
func (s *Server) handleVMPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace"))); !ok {
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
	writeJSON(w, http.StatusOK, eff)
}

// handleNamespacePolicy answers for a whole namespace: pod-selecting policies
// come back conditional rather than resolved.
func (s *Server) handleNamespacePolicy(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	if _, ok := s.resolveProject(w, r, byNamespace(ns)); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.effectivePolicy(ns, nil, false))
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
