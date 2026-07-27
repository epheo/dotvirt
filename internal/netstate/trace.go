package netstate

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/reflect"
)

// TraceWorkload is one in-cluster endpoint, resolved by the caller from
// clusterstate: the labels selectors match, live addresses, and NIC
// attachments for the connectivity check.
type TraceWorkload struct {
	Namespace  string
	Name       string
	NSLabels   map[string]string
	PodLabels  map[string]string
	IPs        []string // live VMI addresses; nil for a stopped VM
	PodNet     bool     // attaches the namespace's primary network
	DefaultNet string   // NAD ref substituted as the default network, "namespace/name"
	Nets       []string // secondary multus refs, "namespace/name"
}

// Trace simulates one flow through the policy planes: the same pure in-memory
// scan Effective does, but rule-level - given a concrete source, destination
// and protocol/port, walk the evaluation order and report each step's verdict
// with the deciding rule.
//
// East-west needs both sides: egress on the source AND ingress on the
// destination, each through admin (by precedence, Pass delegating down), then
// the selecting NetworkPolicies (selection alone isolates: no allowing rule
// means default-deny), then baseline. An external destination runs the egress
// side plus the gateway firewall, and reports the SNAT/route planes.
//
// Deny is only ever certain. A rule that cannot be resolved here (named port,
// a stopped VM's unknown addresses, a DNS rule) stays visible as a
// Conditional step and downgrades the verdict - never silently dropped.
func (s *Snapshot) Trace(src TraceWorkload, dst *TraceWorkload, dstIP, protocol string, port int) model.TraceResult {
	res := model.TraceResult{Steps: []model.TraceStep{}}
	if protocol == "" {
		protocol = "TCP"
	}

	if dst != nil {
		reachable, steps := s.connectivity(src, *dst)
		res.Steps = append(res.Steps, steps...)
		if !reachable {
			res.Verdict = "Unreachable"
			return res
		}
		eg := s.directionWalk("Egress", src, peerTarget{w: dst}, protocol, port)
		in := s.directionWalk("Ingress", *dst, peerTarget{w: &src}, protocol, port)
		res.Steps = append(append(res.Steps, eg.steps...), in.steps...)
		res.Verdict = verdict(eg, in)
		return res
	}

	res.Steps = append(res.Steps, s.externalConnectivity(src, dstIP)...)
	eg := s.directionWalk("Egress", src, peerTarget{ip: dstIP}, protocol, port)
	gw := s.gatewayWalk(src.Namespace, dstIP, protocol, port)
	res.Steps = append(append(res.Steps, eg.steps...), gw.steps...)
	res.Steps = append(res.Steps, s.egressPlaneSteps(src)...)
	res.Verdict = verdict(eg, gw)
	return res
}

// walkResult is one directional walk's steps and outcome. Outcome Deny is
// certain; Conditional means an unresolved rule could change the answer.
type walkResult struct {
	steps   []model.TraceStep
	outcome string // Allow | Deny | Conditional
}

// verdict combines directional outcomes: one certain deny kills the flow;
// otherwise any unresolved rule keeps the answer honest.
func verdict(rs ...walkResult) string {
	for _, r := range rs {
		if r.outcome == "Deny" {
			return "Deny"
		}
	}
	for _, r := range rs {
		if r.outcome == "Conditional" {
			return "Conditional"
		}
	}
	return "Allow"
}

// directionWalk runs one side of the flow (egress rules on the source,
// ingress rules on the destination) through the east-west tiers.
func (s *Snapshot) directionWalk(dir string, subject TraceWorkload, peer peerTarget, protocol string, port int) walkResult {
	field, peerKey := "egress", "to"
	if dir == "Ingress" {
		field, peerKey = "ingress", "from"
	}
	var steps []model.TraceStep
	var condActions []string

	addStep := func(pol model.Policy, rule *model.PolicyRuleView, stage, action, note string, cond, decisive bool) {
		pol.Rules = nil // the step carries the one matched rule, not the whole table
		steps = append(steps, model.TraceStep{
			Stage: stage, Direction: dir, Policy: &pol, Rule: rule,
			Action: action, Conditional: cond, Decisive: decisive, Note: note,
		})
	}
	// decide fixes the outcome: certain only when no unresolved rule above
	// could have decided differently.
	decide := func(action string) walkResult {
		for _, a := range condActions {
			if a != action {
				return walkResult{steps: steps, outcome: "Conditional"}
			}
		}
		return walkResult{steps: steps, outcome: action}
	}

	// adminTier serves both ANP walks: every rule in order, first match decides;
	// Allow/Deny are final, and (admin tier only) Pass hands the flow down. A
	// non-nil return is the walk's outcome; nil means evaluation continues into
	// the tiers below (no decision, or a Pass delegation).
	adminTier := func(policies []*unstructured.Unstructured, baseline bool) *walkResult {
		stage := "admin"
		if baseline {
			stage = "baseline"
		}
		for _, u := range policies {
			sm := matched{m: anpSubjectMatch(u, subject.NSLabels, subject.PodLabels, true)}
			if sm.m == matchNo {
				continue
			}
			if sm.m == matchCond {
				sm.reason = "the policy's subject selector could not be resolved"
			}
			rules, _, _ := unstructured.NestedSlice(u.Object, "spec", field)
			for _, raw := range rules {
				r, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				m := allOf(sm, anpPeerMatch(r[peerKey], peer), anpPortsMatch(r["ports"], protocol, port))
				if m.m == matchNo {
					continue
				}
				action := str(r["action"])
				rv := &model.PolicyRuleView{Direction: dir, Action: action, Peer: adminPeers(r[peerKey]), Ports: portsSummary(r["ports"])}
				if m.m == matchCond {
					addStep(policyFromANP(u, baseline), rv, stage, action, "May match: "+m.reason+".", true, false)
					condActions = append(condActions, action)
					continue
				}
				if !baseline && action == "Pass" {
					addStep(policyFromANP(u, false), rv, stage, action, "Delegates this flow to the project tier.", false, true)
					return nil
				}
				addStep(policyFromANP(u, baseline), rv, stage, action, "", false, true)
				res := decide(action)
				return &res
			}
		}
		return nil
	}

	// Admin tier: every ANP rule in (priority, rule) order. The first match
	// decides - Allow/Deny are final for the flow, Pass hands it down.
	if res := adminTier(s.sortedANPs(), false); res != nil {
		return *res
	}

	// A maybe-matching Pass converges here: matched or not, evaluation
	// continues into the tiers below, so it can no longer change the outcome.
	// (Inside the admin tier it still diverges - a later decisive ANP rule
	// would have been skipped by a matching Pass.)
	kept := condActions[:0]
	for _, a := range condActions {
		if a != "Pass" {
			kept = append(kept, a)
		}
	}
	condActions = kept

	// Project tier: rules across every selecting NetworkPolicy are one allow
	// list. Selection alone isolates the direction - no allowing rule means
	// the tier default-denies the flow.
	var selecting []*unstructured.Unstructured
	definiteSel := 0
	for _, u := range reflect.List(s.netpol) {
		if u.GetNamespace() != subject.Namespace {
			continue
		}
		sel, _, _ := unstructured.NestedMap(u.Object, "spec", "podSelector")
		m := matchSelector(sel, subject.PodLabels)
		if m == matchNo {
			continue
		}
		ing, eg := netpolTypes(u)
		if (dir == "Ingress" && !ing) || (dir == "Egress" && !eg) {
			continue
		}
		if m == matchYes {
			definiteSel++
		}
		selecting = append(selecting, u)
	}
	if len(selecting) > 0 {
		for _, u := range selecting {
			rules, _, _ := unstructured.NestedSlice(u.Object, "spec", field)
			for _, raw := range rules {
				r, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				m := allOf(netpolPeersMatch(r[peerKey], peer, subject.Namespace), netpolPortsMatch(r["ports"], protocol, port))
				if m.m == matchNo {
					continue
				}
				rv := &model.PolicyRuleView{Direction: dir, Action: "Allow", Peer: netpolPeers(r[peerKey]), Ports: portsSummary(r["ports"])}
				if m.m == matchCond {
					addStep(policyFromNetpol(u), rv, "dfw", "Allow", "May match: "+m.reason+".", true, false)
					condActions = append(condActions, "Allow")
					continue
				}
				addStep(policyFromNetpol(u), rv, "dfw", "Allow", "", false, true)
				return decide("Allow")
			}
		}
		note := fmt.Sprintf("Selected by %d project %s for %s; no rule allows this flow.",
			len(selecting), plural(len(selecting), "policy", "policies"), strings.ToLower(dir))
		if definiteSel == 0 {
			// Selection itself unresolved: isolation may not even apply.
			addStep(policyFromNetpol(selecting[0]), nil, "dfw", "Deny", note+" Selection could not be resolved.", true, false)
			return walkResult{steps: steps, outcome: "Conditional"}
		}
		addStep(policyFromNetpol(selecting[0]), nil, "dfw", "Deny", note, false, true)
		return decide("Deny")
	}

	// Baseline tier: reached only when nothing above decided.
	if res := adminTier(reflect.List(s.banp), true); res != nil {
		return *res
	}

	steps = append(steps, model.TraceStep{
		Stage: "default", Direction: dir, Action: "Allow", Decisive: true,
		Note: "No policy matches this flow — the network default allows it.",
	})
	return decide("Allow")
}

// gatewayWalk runs the namespace's EgressFirewall rules in order - the
// gateway tier is first-match, default allow.
func (s *Snapshot) gatewayWalk(ns, dstIP, protocol string, port int) walkResult {
	var steps []model.TraceStep
	var condActions []string
	var fw *unstructured.Unstructured
	for _, u := range reflect.List(s.egressfw) {
		if u.GetNamespace() != ns {
			continue
		}
		fw = u
		rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
		for _, raw := range rules {
			r, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			to, _ := r["to"].(map[string]any)
			var pm matched
			peer := ""
			if c := str(to["cidrSelector"]); c != "" {
				pm, peer = cidrsMatch([]any{c}, peerTarget{ip: dstIP}), c
			} else if d := str(to["dnsName"]); d != "" {
				pm, peer = matched{m: matchCond, reason: "DNS-name rule (" + d + ") — resolution unknown here"}, d
			} else if to["nodeSelector"] != nil {
				pm, peer = matched{m: matchCond, reason: "node-selector rule — whether this address is a cluster node is unknown here"}, "cluster nodes"
			} else {
				// An unrecognized destination form still stays visible - a
				// dropped rule would let a later rule decide with certainty.
				pm, peer = matched{m: matchCond, reason: "a destination form this trace cannot resolve"}, "unresolved destination"
			}
			m := allOf(pm, netpolPortsMatch(r["ports"], protocol, port))
			if m.m == matchNo {
				continue
			}
			action := str(r["type"])
			rv := &model.PolicyRuleView{Direction: "Egress", Action: action, Peer: peer, Ports: portsSummary(r["ports"])}
			pol := policyFromEgressFirewall(u)
			pol.Rules = nil
			if m.m == matchCond {
				steps = append(steps, model.TraceStep{Stage: "gateway", Direction: "Egress", Policy: &pol, Rule: rv,
					Action: action, Conditional: true, Note: "May match: " + m.reason + "."})
				condActions = append(condActions, action)
				continue
			}
			steps = append(steps, model.TraceStep{Stage: "gateway", Direction: "Egress", Policy: &pol, Rule: rv,
				Action: action, Decisive: true})
			for _, a := range condActions {
				if a != action {
					return walkResult{steps: steps, outcome: "Conditional"}
				}
			}
			return walkResult{steps: steps, outcome: action}
		}
	}
	if fw != nil {
		pol := policyFromEgressFirewall(fw)
		pol.Rules = nil
		steps = append(steps, model.TraceStep{Stage: "gateway", Direction: "Egress", Policy: &pol,
			Action: "Allow", Decisive: true, Note: "No gateway rule matches — the gateway defaults to allow."})
	}
	if len(condActions) > 0 {
		return walkResult{steps: steps, outcome: "Conditional"}
	}
	return walkResult{steps: steps, outcome: "Allow"}
}

// egressPlaneSteps reports the informational planes an external flow rides:
// which SNAT pool the traffic leaves under and which route steers it. They
// never gate the verdict.
func (s *Snapshot) egressPlaneSteps(src TraceWorkload) []model.TraceStep {
	snat, routes := s.egressBindings(src.NSLabels, src.PodLabels, true)
	var steps []model.TraceStep
	for _, b := range snat {
		steps = append(steps, planeStep(b, "snat", "SNAT", "Egress leaves source-NATed to this pool."))
	}
	for _, b := range routes {
		steps = append(steps, planeStep(b, "route", "Route", "Egress is steered to this next hop instead of the default gateway."))
	}
	return steps
}

// planeStep renders one egress binding as an informational trace row, detaching
// the policy's single rule into the step's Rule slot.
func planeStep(b egressBinding, stage, action, note string) model.TraceStep {
	pol := b.pol
	var rv *model.PolicyRuleView
	if len(pol.Rules) > 0 {
		rv = &pol.Rules[0]
	}
	pol.Rules = nil
	return model.TraceStep{Stage: stage, Policy: &pol, Rule: rv, Action: action,
		Conditional: b.m == matchCond, Note: note}
}

// connectivity decides whether an east-west path exists at all: both ends
// must sit on the same primary network - isolated primaries drop the flow
// before any policy runs. A shared secondary segment is surfaced as an
// unfiltered bypass either way.
func (s *Snapshot) connectivity(src, dst TraceWorkload) (bool, []model.TraceStep) {
	var steps []model.TraceStep
	srcKey, srcName := s.primaryOf(src)
	dstKey, dstName := s.primaryOf(dst)
	reachable := srcKey != "" && srcKey == dstKey
	switch {
	case reachable:
		steps = append(steps, model.TraceStep{Stage: "connectivity", Action: "Reachable",
			Note: "Both workloads attach " + srcName + "."})
	case srcKey == "" || dstKey == "":
		who := src.Name
		if srcKey != "" {
			who = dst.Name
		}
		steps = append(steps, model.TraceStep{Stage: "connectivity", Action: "Unreachable", Decisive: true,
			Note: who + " has no primary network attachment."})
	default:
		steps = append(steps, model.TraceStep{Stage: "connectivity", Action: "Unreachable", Decisive: true,
			Note: fmt.Sprintf("Isolated primary networks: %s is on %s, %s on %s.", src.Name, srcName, dst.Name, dstName)})
	}
	for _, name := range s.sharedSegments(src.Nets, dst.Nets) {
		steps = append(steps, model.TraceStep{Stage: "segment", Action: "Bypass",
			Note: "Both attach segment " + name + " — an unfiltered layer-2 path; east-west policy does not apply on secondary segments."})
	}
	return reachable, steps
}

// externalConnectivity frames the egress path, flagging VLAN NICs whose
// traffic leaves through the fabric around every control the trace evaluates.
func (s *Snapshot) externalConnectivity(src TraceWorkload, dstIP string) []model.TraceStep {
	steps := []model.TraceStep{{Stage: "connectivity", Action: "Reachable",
		Note: "Cluster egress path to " + dstIP + ", evaluated against the source's egress controls."}}
	for _, ref := range src.Nets {
		if s.isLocalnet(ref) {
			_, name := s.segmentKey(ref)
			steps = append(steps, model.TraceStep{Stage: "segment", Action: "Bypass",
				Note: "NIC on VLAN segment " + name + " — traffic leaving through it bypasses these controls."})
		}
	}
	return steps
}
