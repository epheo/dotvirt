package netstate

import (
	"fmt"
	"strings"

	"github.com/epheo/dotvirt/internal/model"
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

// walk accumulates one pass's steps plus the actions of the maybe-matching
// rules seen so far, so a later certain rule can tell whether an unresolved
// one above could have decided differently.
type walk struct {
	dir         string
	steps       []model.TraceStep
	condActions []string
}

// addStep records a rule-stage step; the step carries the one matched rule,
// not the policy's whole table. pol is nil for the network default.
func (w *walk) addStep(pol *model.Policy, rule *model.PolicyRuleView, stage, action, note string, cond, decisive bool) {
	if pol != nil {
		p := *pol
		p.Rules = nil
		pol = &p
	}
	w.steps = append(w.steps, model.TraceStep{
		Stage: stage, Direction: w.dir, Policy: pol, Rule: rule,
		Action: action, Conditional: cond, Decisive: decisive, Note: note,
	})
}

// maybe records a rule that may match: visible, non-decisive, and remembered
// so a later decision is only certain if it agrees.
func (w *walk) maybe(pol *model.Policy, rule *model.PolicyRuleView, stage, action, reason string) {
	w.addStep(pol, rule, stage, action, "May match: "+reason+".", true, false)
	w.condActions = append(w.condActions, action)
}

// decide fixes the outcome: certain only when no unresolved rule above could
// have decided differently.
func (w *walk) decide(action string) walkResult {
	for _, a := range w.condActions {
		if a != action {
			return w.result("Conditional")
		}
	}
	return w.result(action)
}

func (w *walk) result(outcome string) walkResult {
	return walkResult{steps: w.steps, outcome: outcome}
}

// directionWalk runs one side of the flow (egress rules on the source,
// ingress rules on the destination) through the east-west tiers.
func (s *Snapshot) directionWalk(dir string, subject TraceWorkload, peer peerTarget, protocol string, port int) walkResult {
	w := &walk{dir: dir}

	// adminTier serves both ANP walks: every rule in order, first match decides;
	// Allow/Deny are final, and (admin tier only) Pass hands the flow down. A
	// non-nil return is the walk's outcome; nil means evaluation continues into
	// the tiers below (no decision, or a Pass delegation).
	adminTier := func(policies []*policy, stage string) *walkResult {
		baseline := stage == "baseline"
		for _, b := range bind(policies, subject.NSLabels, subject.PodLabels, true) {
			sm := matched{m: b.m}
			if sm.m == matchCond {
				sm.reason = reasonSubject
			}
			for _, r := range b.pol.rules(dir) {
				m := allOf(sm, r.match(peer, protocol, port))
				if m.m == matchNo {
					continue
				}
				rv := r.view(dir)
				if m.m == matchCond {
					w.maybe(&b.pol.view, &rv, stage, r.action, m.reason)
					continue
				}
				if !baseline && r.action == "Pass" {
					w.addStep(&b.pol.view, &rv, stage, r.action, "Delegates this flow to the project tier.", false, true)
					return nil
				}
				w.addStep(&b.pol.view, &rv, stage, r.action, "", false, true)
				res := w.decide(r.action)
				return &res
			}
		}
		return nil
	}

	// Admin tier: every ANP rule in (priority, rule) order. The first match
	// decides - Allow/Deny are final for the flow, Pass hands it down.
	if res := adminTier(s.sortedANPs(), "admin"); res != nil {
		return *res
	}

	// A maybe-matching Pass converges here: matched or not, evaluation
	// continues into the tiers below, so it can no longer change the outcome.
	// (Inside the admin tier it still diverges - a later decisive ANP rule
	// would have been skipped by a matching Pass.)
	kept := w.condActions[:0]
	for _, a := range w.condActions {
		if a != "Pass" {
			kept = append(kept, a)
		}
	}
	w.condActions = kept

	// Project tier: rules across every selecting NetworkPolicy are one allow
	// list. Selection alone isolates the direction - no allowing rule means
	// the tier default-denies the flow.
	var selecting []bound
	definite := 0
	for _, b := range s.selectingNetpols(subject.Namespace, subject.PodLabels, true) {
		if (dir == "Ingress" && !b.pol.denyIngress) || (dir == "Egress" && !b.pol.denyEgress) {
			continue
		}
		if b.m == matchYes {
			definite++
		}
		selecting = append(selecting, b)
	}
	if len(selecting) > 0 {
		for _, b := range selecting {
			for _, r := range b.pol.rules(dir) {
				m := r.match(peer, protocol, port)
				if m.m == matchNo {
					continue
				}
				rv := r.view(dir)
				if m.m == matchCond {
					w.maybe(&b.pol.view, &rv, "dfw", r.action, m.reason)
					continue
				}
				w.addStep(&b.pol.view, &rv, "dfw", r.action, "", false, true)
				return w.decide(r.action)
			}
		}
		note := fmt.Sprintf("Selected by %d project %s for %s; no rule allows this flow.",
			len(selecting), plural(len(selecting), "policy", "policies"), strings.ToLower(dir))
		first := &selecting[0].pol.view
		if definite == 0 {
			// Selection itself unresolved: isolation may not even apply.
			w.addStep(first, nil, "dfw", "Deny", note+" Selection could not be resolved.", true, false)
			return w.result("Conditional")
		}
		w.addStep(first, nil, "dfw", "Deny", note, false, true)
		return w.decide("Deny")
	}

	// Baseline tier: reached only when nothing above decided.
	if res := adminTier(s.baselines(), "baseline"); res != nil {
		return *res
	}

	w.addStep(nil, nil, "default", "Allow", "No policy matches this flow — the network default allows it.", false, true)
	return w.decide("Allow")
}

// gatewayWalk runs the namespace's EgressFirewall rules in order - the
// gateway tier is first-match, default allow.
func (s *Snapshot) gatewayWalk(ns, dstIP, protocol string, port int) walkResult {
	w := &walk{dir: "Egress"}
	target := peerTarget{ip: dstIP}
	var fw *policy
	for _, p := range s.gateways(ns) {
		fw = p
		for _, r := range p.egress {
			m := r.match(target, protocol, port)
			if m.m == matchNo {
				continue
			}
			rv := r.view("Egress")
			if m.m == matchCond {
				w.maybe(&p.view, &rv, "gateway", r.action, m.reason)
				continue
			}
			w.addStep(&p.view, &rv, "gateway", r.action, "", false, true)
			return w.decide(r.action)
		}
	}
	if fw != nil {
		w.addStep(&fw.view, nil, "gateway", "Allow", "No gateway rule matches — the gateway defaults to allow.", false, true)
	}
	return w.decide("Allow")
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
func planeStep(b bound, stage, action, note string) model.TraceStep {
	pol := b.pol.view
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
