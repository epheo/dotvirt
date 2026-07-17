package netstate

import (
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/epheo/dotvirt/internal/model"
)

// Effective computes the policy chain governing one workload — the same pure
// in-memory scan Policies does, but filtered to what binds the given
// namespace/pod labels and ordered by evaluation: admin ANPs by precedence,
// then the project NetworkPolicies that select the pod, then baseline; plus
// the egress planes. podScoped=false is a namespace-level query: pod selectors
// can't resolve there, so those bindings come back Conditional instead of
// being dropped — a maybe-applying firewall rule is never hidden.
//
// Control-plane binding only: which policies apply and in what order, not a
// per-connection verdict.
func (s *Snapshot) Effective(ns string, nsLabels, podLabels map[string]string, podScoped bool) model.EffectivePolicy {
	eff := model.EffectivePolicy{Namespace: ns}

	// Admin tier, evaluated first, precedence-ordered (lower priority wins).
	var admins []model.PolicyBinding
	for _, u := range listOf(s.anp) {
		if m := anpSubjectMatch(u, nsLabels, podLabels, podScoped); m != matchNo {
			admins = append(admins, model.PolicyBinding{Policy: policyFromANP(u, false), Conditional: m == matchCond})
		}
	}
	sort.Slice(admins, func(i, j int) bool {
		a, b := admins[i].Policy, admins[j].Policy
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.Name < b.Name
	})

	// Project tier: NetworkPolicies in the namespace whose podSelector selects
	// the workload. A definite selection default-denies the directions the
	// policy declares — the fact the panel must surface, since it flips the
	// namespace from open to allowlist.
	var project []model.PolicyBinding
	for _, u := range listOf(s.netpol) {
		if u.GetNamespace() != ns {
			continue
		}
		sel, _, _ := unstructured.NestedMap(u.Object, "spec", "podSelector")
		m := podMatch(sel, podLabels, podScoped)
		if m == matchNo {
			continue
		}
		if m == matchYes {
			ing, eg := netpolTypes(u)
			eff.DefaultDenyIngress = eff.DefaultDenyIngress || ing
			eff.DefaultDenyEgress = eff.DefaultDenyEgress || eg
		}
		project = append(project, model.PolicyBinding{Policy: policyFromNetpol(u), Conditional: m == matchCond})
	}
	sort.Slice(project, func(i, j int) bool { return project[i].Policy.Name < project[j].Policy.Name })

	// Baseline tier, evaluated last.
	var base []model.PolicyBinding
	for _, u := range listOf(s.banp) {
		if m := anpSubjectMatch(u, nsLabels, podLabels, podScoped); m != matchNo {
			base = append(base, model.PolicyBinding{
				Policy:      policyFromANP(u, true),
				Conditional: m == matchCond,
				Note:        "Applies only where no admin or project rule decided.",
			})
		}
	}

	eff.EastWest = append(append(admins, project...), base...)

	// Gateway firewall: the namespace's EgressFirewall (rules are first-match).
	for _, u := range listOf(s.egressfw) {
		if u.GetNamespace() == ns {
			eff.Gateway = append(eff.Gateway, model.PolicyBinding{Policy: policyFromEgressFirewall(u)})
		}
	}

	// Tier-0 SNAT: EgressIPs pinning this namespace; an optional podSelector
	// narrows within it.
	for _, u := range listOf(s.egressip) {
		nsSel, _, _ := unstructured.NestedMap(u.Object, "spec", "namespaceSelector")
		podSel, _, _ := unstructured.NestedMap(u.Object, "spec", "podSelector")
		m := combineMatch(matchSelector(nsSel, nsLabels), podMatch(podSel, podLabels, podScoped))
		if m != matchNo {
			eff.SNAT = append(eff.SNAT, model.PolicyBinding{Policy: policyFromEgressIP(u), Conditional: m == matchCond})
		}
	}

	// Tier-0 routes: external routes steering this namespace's egress.
	for _, u := range listOf(s.extroute) {
		sel, _, _ := unstructured.NestedMap(u.Object, "spec", "from", "namespaceSelector")
		if m := matchSelector(sel, nsLabels); m != matchNo {
			eff.Routes = append(eff.Routes, model.PolicyBinding{Policy: policyFromExtRoute(u), Conditional: m == matchCond})
		}
	}

	return eff
}

// match is a selector's verdict against known labels. matchCond means the
// selector couldn't be resolved here — the caller keeps the binding and marks
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

// matchSelector evaluates a LabelSelector (as stored in an unstructured spec)
// against known labels. Absent/empty selects everything — the API convention
// every kind here shares. Undecodable selectors come back matchCond, not
// matchNo: live objects are apiserver-validated so this is near-impossible,
// but the conservative direction is to keep the row.
func matchSelector(sel map[string]any, lbls map[string]string) match {
	if len(sel) == 0 {
		return matchYes
	}
	var ls metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(sel, &ls); err != nil {
		return matchCond
	}
	sl, err := metav1.LabelSelectorAsSelector(&ls)
	if err != nil {
		return matchCond
	}
	if sl.Matches(labels.Set(lbls)) {
		return matchYes
	}
	return matchNo
}

// podMatch resolves a pod-level selector: empty selects all pods (so it is
// definite even for a namespace-level query); otherwise a namespace-level
// query can only say "the pods matching this".
func podMatch(sel map[string]any, podLabels map[string]string, podScoped bool) match {
	if len(sel) == 0 {
		return matchYes
	}
	if !podScoped {
		return matchCond
	}
	return matchSelector(sel, podLabels)
}

// anpSubjectMatch resolves an ANP/BANP subject (exactly one of namespaces or
// pods) against the workload. No subject matches nothing.
func anpSubjectMatch(u *unstructured.Unstructured, nsLabels, podLabels map[string]string, podScoped bool) match {
	if sel, found, _ := unstructured.NestedMap(u.Object, "spec", "subject", "namespaces"); found {
		return matchSelector(sel, nsLabels)
	}
	if pods, found, _ := unstructured.NestedMap(u.Object, "spec", "subject", "pods"); found {
		nsSel, _ := pods["namespaceSelector"].(map[string]any)
		podSel, _ := pods["podSelector"].(map[string]any)
		return combineMatch(matchSelector(nsSel, nsLabels), podMatch(podSel, podLabels, podScoped))
	}
	return matchNo
}

// netpolTypes reports which directions a NetworkPolicy default-denies for the
// pods it selects, honoring the API defaulting: policyTypes absent means
// Ingress, plus Egress when egress rules are present.
func netpolTypes(u *unstructured.Unstructured) (ingress, egress bool) {
	if types, found, _ := unstructured.NestedStringSlice(u.Object, "spec", "policyTypes"); found && len(types) > 0 {
		for _, t := range types {
			ingress = ingress || t == "Ingress"
			egress = egress || t == "Egress"
		}
		return ingress, egress
	}
	_, egFound, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
	return true, egFound
}
