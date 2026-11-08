package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netstate"
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
	wl, found := s.state.Workload(ns, name)
	if !found {
		fail(w, fmt.Errorf("%w: vm %s/%s", model.ErrNotFound, ns, name))
		return
	}
	eff := s.effectivePolicy(ns, wl.Labels, true)
	eff.VM, eff.Labels, eff.LabelsLive = name, wl.Labels, wl.Live
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
		allowed := s.capable(ctx, id, c, ask(verbCreate, ref, ""))
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

// policyRedactor hides, from a caller without authority over a cluster-tier
// policy's kind, what that policy says beyond the caller's own namespaces:
// its subject namespaces, every selector value its text spells that reaches
// outside the caller's view, and its apply error. The rules stay - a rule
// that governs the caller's workload is never hidden - while the full rollup
// is what /api/policies gates on the same authority.
type policyRedactor struct {
	can  func(model.PolicyKind) bool
	view *netstate.Redaction
	snap *netstate.Snapshot
}

// policyRedactor is the caller's redactor: their visible namespaces, the
// projects those belong to, every namespace the snapshot holds to test
// selectors against, and whether they may read nodes (the cached review the
// node views share, asked only when a node selector renders). A failed
// visibility read hides every name.
func (s *Server) policyRedactor(ctx context.Context, id auth.Identity, c *cluster.Client) policyRedactor {
	projects := map[string]bool{}
	visible, err := s.visibleFor(ctx, id, c)
	if err == nil {
		for _, p := range s.resolver.Resolve(s.state.Namespaces(), visible) {
			projects[p.Name] = true
		}
	}
	return policyRedactor{
		can:  s.clusterAuthority(ctx, id, c),
		snap: s.netstate,
		view: &netstate.Redaction{
			ProjectLabel: s.cfg.ProjectLabel,
			Namespace:    func(ns string) bool { return visible[ns] },
			Project:      func(name string) bool { return projects[name] },
			Namespaces:   sync.OnceValue(s.namespaceLabels),
			Nodes:        sync.OnceValue(func() bool { return s.capable(ctx, id, c, ask(verbList, ssarNodes, "")) }),
		},
	}
}

// namespaceLabels is every namespace as the SA snapshot holds it: the project
// namespaces with their labels, every other one by name alone. One list serves
// every caller until the snapshot's namespaces move; none may modify it.
func (s *Server) namespaceLabels() []netstate.NamespaceLabels {
	s.nsListMu.Lock()
	defer s.nsListMu.Unlock()
	gen := s.state.NamespacesGeneration()
	if s.nsList != nil && s.nsListGen == gen {
		return s.nsList
	}
	held := s.state.Namespaces()
	all := s.state.AllNamespaces()
	out := make([]netstate.NamespaceLabels, 0, len(all))
	seen := make(map[string]bool, len(held))
	for _, ns := range held {
		out = append(out, netstate.NamespaceLabels{Name: ns.Name, Labels: ns.Labels})
		seen[ns.Name] = true
	}
	for name := range all {
		if !seen[name] {
			out = append(out, netstate.NamespaceLabels{Name: name})
		}
	}
	s.nsList, s.nsListGen = out, gen
	return out
}

// apply redacts p, and rule when it is one of p's rows (a trace step's).
func (pr policyRedactor) apply(p *model.Policy, rule *model.PolicyRuleView) {
	if _, cluster := clusterPolicyRefs[p.Kind]; !cluster || pr.can(p.Kind) {
		return
	}
	p.Namespaces = nil
	if p.SyncError != "" {
		p.SyncError = redactedSyncError
	}
	if pr.snap != nil {
		pr.snap.Redact(p, rule, pr.view)
	}
}

// redactedSyncError stands in for a cluster-tier policy's apply error, which
// can name other tenants' namespaces.
const redactedSyncError = "the platform could not apply this policy"

func (s *Server) redactEffective(ctx context.Context, sc scope, eff *model.EffectivePolicy) {
	pr := s.policyRedactor(ctx, sc.id, sc.cluster)
	for _, bs := range [][]model.PolicyBinding{eff.EastWest, eff.Gateway, eff.SNAT, eff.Routes} {
		for i := range bs {
			pr.apply(&bs[i].Policy, nil)
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
