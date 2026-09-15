package netstate

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/epheo/dotvirt/internal/model"
)

// Effective computes the policy chain governing one workload - the same pure
// in-memory scan Policies does, but filtered to what binds the given
// namespace/pod labels and ordered by evaluation: admin ANPs by precedence,
// then the project NetworkPolicies that select the pod, then baseline; plus
// the egress planes. podScoped=false is a namespace-level query: pod selectors
// can't resolve there, so those bindings come back Conditional instead of
// being dropped - a maybe-applying firewall rule is never hidden.
//
// Control-plane binding only: which policies apply and in what order, not a
// per-connection verdict.
func (s *Snapshot) Effective(ns string, nsLabels, podLabels map[string]string, podScoped bool) model.EffectivePolicy {
	eff := model.EffectivePolicy{Namespace: ns}

	for _, b := range bind(s.sortedANPs(), nsLabels, podLabels, podScoped) {
		eff.EastWest = append(eff.EastWest, b.binding(""))
	}
	// A definite selection default-denies the directions the policy declares
	// - the fact the panel must surface, since it flips the namespace from
	// open to allowlist.
	for _, b := range s.selectingNetpols(ns, podLabels, podScoped) {
		if b.m == matchYes {
			eff.DefaultDenyIngress = eff.DefaultDenyIngress || b.pol.denyIngress
			eff.DefaultDenyEgress = eff.DefaultDenyEgress || b.pol.denyEgress
		}
		eff.EastWest = append(eff.EastWest, b.binding(""))
	}
	for _, b := range bind(s.baselines(), nsLabels, podLabels, podScoped) {
		eff.EastWest = append(eff.EastWest, b.binding("Applies only where no admin or project rule decided."))
	}

	// Gateway firewall: the namespace's EgressFirewall (rules are first-match).
	for _, p := range s.gateways(ns) {
		eff.Gateway = append(eff.Gateway, model.PolicyBinding{Policy: p.view})
	}

	// Tier-0: the SNAT pools and external routes binding this namespace.
	snat, routes := s.egressBindings(nsLabels, podLabels, podScoped)
	for _, b := range snat {
		eff.SNAT = append(eff.SNAT, b.binding(""))
	}
	for _, b := range routes {
		eff.Routes = append(eff.Routes, b.binding(""))
	}
	return eff
}

// bound pairs a policy with its subject's verdict against a workload.
type bound struct {
	pol *policy
	m   match
}

func (b bound) binding(note string) model.PolicyBinding {
	return model.PolicyBinding{Policy: b.pol.view, Conditional: b.m == matchCond, Note: note}
}

// bind keeps the policies whose subject may apply to the workload, in the
// order given - the one filter every tier and plane shares.
func bind(policies []*policy, nsLabels, podLabels map[string]string, podScoped bool) []bound {
	var out []bound
	for _, p := range policies {
		if m := p.subject.match(nsLabels, podLabels, podScoped); m != matchNo {
			out = append(out, bound{p, m})
		}
	}
	return out
}

// selectingNetpols returns the namespace's NetworkPolicies whose podSelector
// selects the workload, by name - the project tier both Effective and Trace
// walk. Selection alone isolates the declared directions; whether a rule then
// allows a given flow is the trace's question.
func (s *Snapshot) selectingNetpols(ns string, podLabels map[string]string, podScoped bool) []bound {
	return bind(inNamespace(s.netpol, ns, decodeNetpol), nil, podLabels, podScoped)
}

// egressBindings resolves which SNAT pools (EgressIP: namespaceSelector plus an
// optional podSelector narrowing within it) and external routes bind a workload
// - the one query behind the Effective view and the trace's egress planes.
func (s *Snapshot) egressBindings(nsLabels, podLabels map[string]string, podScoped bool) (snat, routes []bound) {
	return bind(decoded(s.egressip, decodeEgressIP), nsLabels, podLabels, podScoped),
		bind(decoded(s.extroute, decodeExtRoute), nsLabels, podLabels, podScoped)
}

// match is a selector's verdict against known labels. matchCond means the
// selector couldn't be resolved here - the caller keeps the binding and marks
// it conditional, because hiding it would misstate the firewall.
type match int

const (
	matchNo match = iota
	matchYes
	matchCond
)

func combineMatch(a, b match) match {
	if a == matchNo || b == matchNo {
		return matchNo
	}
	if a == matchCond || b == matchCond {
		return matchCond
	}
	return matchYes
}

// match resolves the subject against a workload: the namespace selector
// against its namespace labels, the pod selector against its pod labels.
func (s subject) match(nsLabels, podLabels map[string]string, podScoped bool) match {
	if s.none {
		return matchNo
	}
	return combineMatch(matchSelector(s.ns, nsLabels), podMatch(s.pod, podLabels, podScoped))
}

// matchSelector evaluates a LabelSelector against known labels. Absent/empty
// selects everything - the API convention every kind here shares. A selector
// the library rejects comes back matchCond, not matchNo: live objects are
// apiserver-validated so this is near-impossible, but the conservative
// direction is to keep the row.
func matchSelector(sel *metav1.LabelSelector, lbls map[string]string) match {
	if emptySelector(sel) {
		return matchYes
	}
	sl, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return matchCond
	}
	if sl.Matches(labels.Set(lbls)) {
		return matchYes
	}
	return matchNo
}

func emptySelector(sel *metav1.LabelSelector) bool {
	return sel == nil || (len(sel.MatchLabels) == 0 && len(sel.MatchExpressions) == 0)
}

// podMatch resolves a pod-level selector: empty selects all pods (so it is
// definite even for a namespace-level query); otherwise a namespace-level
// query can only say "the pods matching this".
func podMatch(sel *metav1.LabelSelector, podLabels map[string]string, podScoped bool) match {
	if emptySelector(sel) {
		return matchYes
	}
	if !podScoped {
		return matchCond
	}
	return matchSelector(sel, podLabels)
}
