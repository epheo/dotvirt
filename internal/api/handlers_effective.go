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

// clusterAuthority reports per kind whether the caller may verb that
// cluster-tier kind cluster-wide, each a review cached per (token, kind,
// verb). A failed review reads as no.
func (s *Server) clusterAuthority(ctx context.Context, id auth.Identity, c *cluster.Client, verb string) func(model.PolicyKind) bool {
	return func(k model.PolicyKind) bool {
		ref, ok := clusterPolicyRefs[k]
		return ok && s.capable(ctx, id, c, ask(verb, ref, ""))
	}
}

// clusterPolicyRefs is each cluster-tier policy kind's resource: create on it
// reads a policy's full text, list its identity, as the apiserver would.
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
// is what /api/policies gates on the same authority. From a caller who may
// not list the kind, as a namespace admin may not, it withholds the policy's
// identity too. One serves one response.
type policyRedactor struct {
	can   func(model.PolicyKind) bool   // create on the kind
	named func(model.PolicyKind) bool   // list on the kind
	lists func(backing, ns string) bool // list on a namespaced row's kind, where it lives
	view  *netstate.Redaction
	snap  *netstate.Snapshot
	anon  withheld
}

// policyRedactor is the caller's redactor: their visible namespaces, the
// projects those belong to, every namespace the snapshot holds to test
// selectors against, and whether they may read nodes (the cached review the
// node views share, asked only when a node selector renders). A failed
// visibility read hides every name.
func (s *Server) policyRedactor(ctx context.Context, id auth.Identity, c *cluster.Client) *policyRedactor {
	projects := map[string]bool{}
	v, err := s.viewFor(ctx, id, c)
	visible := v.names
	if err == nil {
		for _, p := range s.resolver.Resolve(s.state.Namespaces(), visible) {
			projects[p.Name] = true
		}
	}
	return &policyRedactor{
		lists: v.listsBacking,
		can:   s.clusterAuthority(ctx, id, c, verbCreate),
		named: s.clusterAuthority(ctx, id, c, verbList),
		snap:  s.netstate,
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
func (pr *policyRedactor) apply(p *model.Policy, rule *model.PolicyRuleView) {
	if _, cluster := clusterPolicyRefs[p.Kind]; !cluster {
		if p.Namespace != "" && !pr.lists(p.Backing, p.Namespace) {
			pr.redact(p, rule)
			pr.withhold(p)
		}
		return
	}
	if !pr.can(p.Kind) {
		pr.redact(p, rule)
	}
	if !pr.named(p.Kind) {
		pr.withhold(p)
	}
}

// redact leaves what p and rule do, without what they name.
func (pr *policyRedactor) redact(p *model.Policy, rule *model.PolicyRuleView) {
	p.Namespaces = nil
	if p.SyncError != "" {
		p.SyncError = redactedSyncError
	}
	if pr.snap != nil {
		pr.snap.Redact(p, rule, pr.view) // finds the policy by name: before withhold
	}
}

// withhold replaces what names the policy object: its name by a label, its
// source file, and its apply error, which Argo words with the object's name.
// What it does and whether it applies stay.
func (pr *policyRedactor) withhold(p *model.Policy) {
	p.Name, p.Withheld, p.SourceFile = pr.anon.label(p), true, ""
	if p.SyncError != "" {
		p.SyncError = redactedSyncError
	}
}

// redactedSyncError stands in for a cluster-tier policy's apply error, which
// can name other tenants' namespaces.
const redactedSyncError = "the platform could not apply this policy"

// withheld labels the policies one response withholds: by kind, numbered from
// the second policy sharing a label in the order the response meets them, so
// its rows stay distinct and no label derives from a name.
type withheld struct {
	labels map[string]string // backing and name: its label
	held   map[string]int    // label: the policies holding it
}

func (w *withheld) label(p *model.Policy) string {
	k := p.Backing + "\x00" + p.Name
	if l, ok := w.labels[k]; ok {
		return l
	}
	if w.labels == nil {
		w.labels, w.held = map[string]string{}, map[string]int{}
	}
	l := withheldLabel(p)
	w.held[l]++
	if n := w.held[l]; n > 1 {
		l = fmt.Sprintf("%s (%d)", l, n)
	}
	w.labels[k] = l
	return l
}

func withheldLabel(p *model.Policy) string {
	switch p.Kind {
	case model.PolicyAdmin:
		return fmt.Sprintf("cluster rule, priority %d", p.Priority)
	case model.PolicyBaseline:
		return "baseline cluster rule"
	case model.PolicyEgressIP:
		return "fixed outbound IP"
	case model.PolicyRoute:
		return "external route"
	case model.PolicyProjectRule:
		return "project rule"
	case model.PolicyGateway:
		return "gateway rule"
	}
	return "cluster policy"
}

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
