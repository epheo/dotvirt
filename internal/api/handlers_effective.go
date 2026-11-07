package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// The effective-policy answer: what governs one workload, in evaluation order.
// Served entirely from the SA snapshots (netstate policies + clusterstate
// labels) - no per-request cluster read; resolveProject gates the namespace
// exactly like the other detail views.

// handleVMPolicy answers for one VM, matching pod selectors against the labels
// its virt-launcher pod carries (live VMI, else the manifest template's).
func (s *Server) handleVMPolicy(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	lbls, live, found := s.state.WorkloadLabels(ns, name)
	if !found {
		fail(w, fmt.Errorf("%w: vm %s/%s", model.ErrNotFound, ns, name))
		return
	}
	eff := s.effectivePolicy(ns, lbls, true)
	eff.VM, eff.Labels, eff.LabelsLive = name, lbls, live
	s.redactEffective(r.Context(), sc, &eff)
	writeJSON(w, eff)
}

// handleNamespacePolicy answers for a whole namespace: pod-selecting policies
// come back conditional rather than resolved.
func (s *Server) handleNamespacePolicy(w http.ResponseWriter, r *http.Request) {
	sc, ns, _, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	eff := s.effectivePolicy(ns, nil, false)
	s.redactEffective(r.Context(), sc, &eff)
	writeJSON(w, eff)
}

// clusterAuthority reports per kind whether the caller may read the
// cluster-tier rollup - the same create-SSAR /api/policies and the create
// routes enforce, cached per (token, kind).
func (s *Server) clusterAuthority(ctx context.Context, id auth.Identity, c *cluster.Client) func(model.PolicyKind) bool {
	return func(k model.PolicyKind) bool {
		ref, ok := clusterPolicyRefs[k]
		if !ok {
			return false
		}
		allowed, _ := s.can(ctx, id, c, verbCreate, ref, "")
		return allowed
	}
}

// clusterPolicyRefs is the create authority over each cluster-tier policy kind.
var clusterPolicyRefs = map[model.PolicyKind]ssarRef{
	model.PolicyAdmin:    ssarANP,
	model.PolicyBaseline: ssarBANP,
	model.PolicyEgressIP: ssarEgressIP,
	model.PolicyRoute:    ssarExtRoute,
}

// redactSubjects strips what a cluster-tier policy says about other tenants
// for callers without authority over the kind: its subject namespaces, the
// target summary that spells the same selector, and the apply error text. The
// rules stay - a rule that governs the caller's workload is never hidden - but
// where else an admin policy applies is the rollup-tier information
// /api/policies gates, so the same authority gates it here.
func redactSubjects(can func(model.PolicyKind) bool, p *model.Policy) {
	if _, cluster := clusterPolicyRefs[p.Kind]; !cluster || can(p.Kind) {
		return
	}
	p.Namespaces = nil
	if p.Target != "" {
		p.Target = redactedTarget
	}
	if p.SyncError != "" {
		p.SyncError = redactedSyncError
	}
}

// redactedTarget and redactedSyncError stand in for a cluster-tier policy's
// subject summary and apply error, which can name other tenants' namespaces.
const (
	redactedTarget    = "selected namespaces"
	redactedSyncError = "the platform could not apply this policy"
)

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
	eff := s.netstate.Effective(ns, s.state.NamespaceLabels(ns), podLabels, podScoped)
	for _, bs := range [][]model.PolicyBinding{eff.EastWest, eff.Gateway, eff.SNAT, eff.Routes} {
		for i := range bs {
			s.policyDrift(&bs[i].Policy)
		}
	}
	return eff
}
