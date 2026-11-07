package netstate

import (
	"sort"

	"github.com/epheo/dotvirt/internal/model"
)

// NetworkRules answers which rules bear on the VMs of one network, given the
// namespaces it reaches: the policies binding those namespaces (as Effective
// lists them, pod selectors conditional), then the rules elsewhere that name
// those namespaces' VMs as their other end, each read from the network's side.
// A policy listed as binding is never repeated as a peer rule.
//
// red renders the peer rules' sentences for one caller (nil renders
// everything); policies and rows come out whole, for the caller's redactor.
func (s *Snapshot) NetworkRules(namespaces []string, nsLabels func(string) map[string]string, red *Redaction) model.NetworkRules {
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
				if pr, ok := peerRule(p, r, dir, namespaces, nsLabels, red); ok {
					out.Reaches = append(out.Reaches, pr)
				}
			}
		}
	}
	sort.SliceStable(out.Reaches, func(i, j int) bool {
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

// peerRule reads one rule from the side of the network's namespaces: the
// first peer alternative that may select their VMs decides the sentence.
func peerRule(p *policy, r rule, dir string, namespaces []string, nsLabels func(string) map[string]string, red *Redaction) (model.PeerRule, bool) {
	for _, pe := range r.peers {
		if pe.kind != peerSelector {
			continue
		}
		best := matchNo
		for _, ns := range namespaces {
			if pe.namespace != "" && pe.namespace != ns {
				continue
			}
			m := combineMatch(matchSelector(pe.ns, nsLabels(ns)), podMatch(pe.pod, nil, false))
			if m == matchYes || (m == matchCond && best == matchNo) {
				best = m
			}
		}
		if best == matchNo {
			continue
		}
		// Policy and Rule come out whole, for the caller's redactor to match
		// and re-render; the sentence is rendered for the caller here.
		_, applies, _ := p.render(red)
		return model.PeerRule{
			Policy:      p.view,
			Rule:        r.view(dir, nil),
			Text:        r.peerText(dir, pe, applies, red),
			Conditional: best == matchCond,
		}, true
	}
	return model.PeerRule{}, false
}

func policyKey(p model.Policy) string { return p.Backing + ":" + p.Namespace + ":" + p.Name }

// evalRank orders tiers as traffic meets them: admin rules, project rules,
// the baseline, then the outbound planes.
func evalRank(k model.PolicyKind) int {
	switch k {
	case model.PolicyAdmin:
		return 0
	case model.PolicyDFW:
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
