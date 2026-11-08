package netstate

import (
	"slices"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
)

// NetworkRules answers which rules bear on the VMs of one network, given the
// namespaces it reaches: the policies binding those namespaces (as Effective
// lists them, pod selectors conditional), then the rules elsewhere that name
// those namespaces' VMs as their other end, each read from the network's side.
// A policy listed as binding is never repeated as a peer rule.
//
// red renders the cluster rules' peer sentences for one caller (nil renders
// everything); a namespaced policy's read whole, as the policy plane shows it
// to whoever sees its namespace. Policies and rows come out whole, for the
// caller's redactor.
// projectLabel is the namespace label naming a project: a selector on it
// names the project, as a namespace list names its namespaces.
func (s *Snapshot) NetworkRules(namespaces []string, nsLabels func(string) map[string]string, red *Redaction, projectLabel string) model.NetworkRules {
	out := model.NetworkRules{Namespaces: namespaces, Applies: []model.PolicyBinding{}, Reaches: []model.PeerRule{}}
	bound := map[string]int{} // policy identity -> index in Applies
	for _, ns := range namespaces {
		eff := s.Effective(ns, nsLabels(ns), nil, false)
		for _, list := range [][]model.PolicyBinding{eff.EastWest, eff.Gateway, eff.SNAT, eff.Routes} {
			for _, b := range list {
				k := policyKey(b.Policy)
				if i, seen := bound[k]; seen {
					// Definite anywhere is definite: some of the network's VMs are bound.
					out.Applies[i].Conditional = out.Applies[i].Conditional && b.Conditional
					continue
				}
				bound[k] = len(out.Applies)
				out.Applies = append(out.Applies, b)
			}
		}
	}
	sort.SliceStable(out.Applies, func(i, j int) bool {
		a, b := out.Applies[i].Policy, out.Applies[j].Policy
		if evalRank(a.Kind) != evalRank(b.Kind) {
			return evalRank(a.Kind) < evalRank(b.Kind)
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})

	var peers []*policy
	peers = append(peers, s.sortedANPs()...)
	peers = append(peers, decoded(s.netpol, decodeNetpol)...)
	peers = append(peers, s.baselines()...)
	for _, p := range peers {
		if _, seen := bound[policyKey(p.view)]; seen {
			continue
		}
		for _, dir := range []string{"Ingress", "Egress"} {
			for _, r := range p.rules(dir) {
				if pr, ok := peerRule(p, r, dir, namespaces, nsLabels, red, projectLabel); ok {
					out.Reaches = append(out.Reaches, pr)
				}
			}
		}
	}
	sort.SliceStable(out.Reaches, func(i, j int) bool {
		if out.Reaches[i].Wildcard != out.Reaches[j].Wildcard {
			return !out.Reaches[i].Wildcard
		}
		a, b := out.Reaches[i].Policy, out.Reaches[j].Policy
		if evalRank(a.Kind) != evalRank(b.Kind) {
			return evalRank(a.Kind) < evalRank(b.Kind)
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

// peerRule reads one rule from the side of the network's namespaces. A peer
// that names one of them (by namespace, or by project) decides the sentence;
// failing that, the first that merely matches - an "any namespace" or label
// selector - does, and the rule is marked a wildcard.
func peerRule(p *policy, r rule, dir string, namespaces []string, nsLabels func(string) map[string]string, red *Redaction, projectLabel string) (model.PeerRule, bool) {
	var picked *peer
	pickedMatch, named := matchNo, false
	for i := range r.peers {
		pe := &r.peers[i]
		if pe.kind != peerSelector {
			continue
		}
		best, names := matchNo, false
		for _, ns := range namespaces {
			if pe.namespace != "" && pe.namespace != ns {
				continue
			}
			m := combineMatch(matchSelector(pe.ns, nsLabels(ns)), podMatch(pe.pod, nil, false))
			if m == matchNo {
				continue
			}
			if m == matchYes || best == matchNo {
				best = m
			}
			names = names || pe.namespace == ns || namesNamespace(pe.ns, ns, nsLabels(ns), projectLabel)
		}
		if best == matchNo || (picked != nil && (named || !names)) {
			continue
		}
		picked, pickedMatch, named = pe, best, names
	}
	if picked == nil {
		return model.PeerRule{}, false
	}
	// Policy and Rule come out whole, for the caller's redactor to match and
	// re-render; the sentence is rendered for the caller here.
	if p.view.Namespace != "" {
		red = nil
	}
	applies := p.subject.applies(p.view.Namespace, red)
	return model.PeerRule{
		Policy:      p.view,
		Rule:        r.view(dir, nil),
		Text:        r.peerText(dir, *picked, applies, red),
		Conditional: pickedMatch == matchCond,
		Wildcard:    !named,
	}, true
}

// namesNamespace reports whether a namespace selector names ns outright: its
// name in the list netgen writes, or its project's label value.
func namesNamespace(sel *metav1.LabelSelector, ns string, labels map[string]string, projectLabel string) bool {
	if slices.Contains(netgen.NamespaceNames(sel), ns) {
		return true
	}
	if sel == nil || projectLabel == "" {
		return false
	}
	v, ok := sel.MatchLabels[projectLabel]
	return ok && v != "" && labels[projectLabel] == v
}

func policyKey(p model.Policy) string { return p.Backing + ":" + p.Namespace + ":" + p.Name }

// evalRank orders tiers as traffic meets them: admin rules, project rules,
// the baseline, then the outbound planes.
func evalRank(k model.PolicyKind) int {
	switch k {
	case model.PolicyAdmin:
		return 0
	case model.PolicyProjectRule:
		return 1
	case model.PolicyBaseline:
		return 2
	case model.PolicyGateway:
		return 3
	case model.PolicyEgressIP:
		return 4
	case model.PolicyRoute:
		return 5
	}
	return 6
}
