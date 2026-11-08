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
	PodLabels  map[string]string // the virt-launcher pod's
	IPs        []string          // the primary NIC's live addresses; nil for a stopped VM
	PodNet     bool              // attaches the namespace's primary network
	DefaultNet string            // NAD ref substituted as the default network, "namespace/name"
	Nets       []string          // secondary multus refs, "namespace/name"
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
// side plus the outbound rules, and reports the SNAT/route planes; a VLAN
// NIC is a path none of them filter.
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
		decided, steps := s.connectivity(src, *dst)
		res.Steps = append(res.Steps, steps...)
		if decided != "" {
			res.Verdict = decided
			return res
		}
		eg := s.directionWalk("Egress", src, peerTarget{w: dst}, protocol, port)
		in := s.directionWalk("Ingress", *dst, peerTarget{w: &src}, protocol, port)
		res.Steps = append(append(res.Steps, eg.steps...), in.steps...)
		res.Verdict = verdict(eg, in)
		return res
	}

	steps, decided, vlan := s.externalConnectivity(src, dstIP)
	res.Steps = append(res.Steps, steps...)
	if decided != "" {
		res.Verdict = decided
		return res
	}
	// Both must pass: OVN-K runs the admin, project and baseline egress rules
	// as from-lport ACLs in the source switch's ingress pipeline and the
	// EgressFirewall as to-lport ACLs in its egress pipeline, each stage with
	// its own tier walk, so an admin Allow never skips the gateway.
	eg := s.directionWalk("Egress", src, peerTarget{ip: dstIP}, protocol, port)
	gw := s.gatewayWalk(src.Namespace, dstIP, protocol, port)
	res.Steps = append(append(res.Steps, eg.steps...), gw.steps...)
	res.Steps = append(res.Steps, s.egressPlaneSteps(src)...)
	res.Verdict = verdict(eg, gw)
	if vlan && res.Verdict == "Deny" {
		res.Verdict = "Conditional" // the guest may route the flow out its VLAN NIC
	}
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

// candidate is a rule that may match the flow, judged against both the
// policy's subject and the rule itself.
type candidate struct {
	pol *policy
	r   rule
	m   matched
}

// candidates returns the bound policy's rules that may match the flow, in
// order, through the first certain match: nothing after it can decide. An
// unresolved subject leaves every rule unresolved.
func (b bound) candidates(dir string, peer peerTarget, protocol string, port int) []candidate {
	sel := matched{m: b.m}
	if sel.m == matchCond {
		sel.reason = reasonSubject
	}
	var out []candidate
	for _, r := range b.pol.rules(dir) {
		m := allOf(sel, r.match(peer, protocol, port))
		if m.m == matchNo {
			continue
		}
		out = append(out, candidate{b.pol, r, m})
		if m.m == matchYes {
			break
		}
	}
	return out
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
		bs := bind(policies, subject.NSLabels, subject.PodLabels, true)
		for len(bs) > 0 {
			n := 1
			for !baseline && n < len(bs) && bs[n].pol.view.Priority == bs[0].pol.view.Priority {
				n++
			}
			var cands []candidate
			deciders := 0
			for _, b := range bs[:n] {
				if c := b.candidates(dir, peer, protocol, port); len(c) > 0 {
					cands = append(cands, c...)
					deciders++
				}
			}
			bs = bs[n:]
			if deciders > 1 {
				// OVN-K maps equal-priority policies onto one ACL priority
				// range, where OVN's pick between matching ACLs is undefined:
				// none of their rules decides with certainty.
				for _, c := range cands {
					reason := c.m.reason
					if c.m.m == matchYes {
						reason = reasonEqualPriority
					}
					rv := c.r.view(dir, nil)
					w.maybe(&c.pol.view, &rv, stage, c.r.action, reason)
				}
				continue
			}
			for _, c := range cands {
				rv := c.r.view(dir, nil)
				if c.m.m == matchCond {
					w.maybe(&c.pol.view, &rv, stage, c.r.action, c.m.reason)
					continue
				}
				if !baseline && c.r.action == "Pass" {
					w.addStep(&c.pol.view, &rv, stage, c.r.action, "Delegates this flow to the project tier.", false, true)
					return nil
				}
				w.addStep(&c.pol.view, &rv, stage, c.r.action, "", false, true)
				res := w.decide(c.r.action)
				return &res
			}
		}
		return nil
	}

	// OVN-K never programs a priority past MaxANPPriority, yet an earlier
	// in-range version keeps its ACLs: such a policy applies at an unknown
	// precedence or not at all, so its rules only ever weigh as maybes.
	anps, unenforced := s.enforcedANPs()
	for _, b := range bind(unenforced, subject.NSLabels, subject.PodLabels, true) {
		for _, c := range b.candidates(dir, peer, protocol, port) {
			rv := c.r.view(dir, nil)
			w.maybe(&c.pol.view, &rv, "admin", c.r.action, unenforcedReason(c.pol.view.Priority))
		}
	}

	// Admin tier: every ANP rule in (priority, rule) order. The first match
	// decides - Allow/Deny are final for the flow, Pass hands it down.
	if res := adminTier(anps, "admin"); res != nil {
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
			for _, c := range b.candidates(dir, peer, protocol, port) {
				rv := c.r.view(dir, nil)
				if c.m.m == matchCond {
					w.maybe(&b.pol.view, &rv, "dfw", c.r.action, c.m.reason)
					continue
				}
				w.addStep(&b.pol.view, &rv, "dfw", c.r.action, "", false, true)
				return w.decide(c.r.action)
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

	w.addStep(nil, nil, "default", "Allow", "No policy matches this flow - the network default allows it.", false, true)
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
			rv := r.view("Egress", nil)
			if m.m == matchCond {
				w.maybe(&p.view, &rv, "gateway", r.action, m.reason)
				continue
			}
			w.addStep(&p.view, &rv, "gateway", r.action, "", false, true)
			return w.decide(r.action)
		}
	}
	if fw != nil {
		w.addStep(&fw.view, nil, "gateway", "Allow", "No gateway rule matches - the gateway defaults to allow.", false, true)
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

// connectivity decides whether an east-west path exists: both ends must sit
// on the same primary network, or isolated primaries drop the flow before any
// policy runs. A shared secondary network is an unfiltered layer-2 bypass;
// without a shared primary it is the only path, so the answer is Conditional,
// never a certain Unreachable. decided is "" when the policy walk decides.
func (s *Snapshot) connectivity(src, dst TraceWorkload) (decided string, steps []model.TraceStep) {
	srcKey, srcName := s.primaryOf(src)
	dstKey, dstName := s.primaryOf(dst)
	reachable := srcKey != "" && srcKey == dstKey
	shared := s.sharedNetworks(src.Nets, dst.Nets)
	if !reachable {
		decided = "Unreachable"
		if len(shared) > 0 {
			decided = "Conditional"
		}
	}
	switch {
	case reachable:
		steps = append(steps, model.TraceStep{Stage: "connectivity", Action: "Reachable",
			Note: "Both workloads attach " + srcName + "."})
	case srcKey == "" || dstKey == "":
		who := src.Name
		if srcKey != "" {
			who = dst.Name
		}
		steps = append(steps, model.TraceStep{Stage: "connectivity", Action: "Unreachable", Decisive: len(shared) == 0,
			Note: who + " has no primary network attachment."})
	default:
		steps = append(steps, model.TraceStep{Stage: "connectivity", Action: "Unreachable", Decisive: len(shared) == 0,
			Note: fmt.Sprintf("Isolated primary networks: %s is on %s, %s on %s.", src.Name, srcName, dst.Name, dstName)})
	}
	for _, name := range shared {
		steps = append(steps, model.TraceStep{Stage: "segment", Action: "Bypass", Conditional: !reachable,
			Note: "Both attach network " + name + " - an unfiltered layer-2 path; workload policy does not apply on secondary networks."})
	}
	return decided, steps
}

// externalConnectivity frames the egress path. The cluster controls filter
// only the primary NIC (the pod network, or a default multus binding that is
// not localnet); a VLAN NIC reaches the fabric around all of them. decided is
// the verdict when no primary NIC exists, so no cluster control sees the flow;
// vlan reports a VLAN NIC, which leaves a cluster Deny inconclusive.
func (s *Snapshot) externalConnectivity(src TraceWorkload, dstIP string) (steps []model.TraceStep, decided string, vlan bool) {
	cluster := src.PodNet || (src.DefaultNet != "" && !s.isLocalnet(src.DefaultNet))
	refs := src.Nets
	if src.DefaultNet != "" && !cluster {
		refs = append([]string{src.DefaultNet}, src.Nets...)
	}
	var bypasses []model.TraceStep
	for _, ref := range refs {
		if s.isLocalnet(ref) {
			_, name := s.networkIdentity(ref)
			bypasses = append(bypasses, model.TraceStep{Stage: "segment", Action: "Bypass", Conditional: true,
				Note: "NIC on VLAN network " + name + " - the guest can route this flow out of it, around every control evaluated here."})
		}
	}
	vlan = len(bypasses) > 0
	switch {
	case cluster:
		steps = append(steps, model.TraceStep{Stage: "connectivity", Action: "Reachable",
			Note: "Cluster egress path to " + dstIP + ", evaluated against the source's egress controls."})
	case len(refs) > 0:
		// A secondary network has no cluster egress, but a VLAN, or a router
		// on the network, may still carry the flow.
		decided = "Conditional"
		steps = append(steps, model.TraceStep{Stage: "connectivity", Action: "Bypass", Conditional: true,
			Note: src.Name + " has no primary network: traffic to " + dstIP + " can leave only through a secondary network, which no cluster control filters. The network behind it decides."})
	default:
		decided = "Unreachable"
		steps = append(steps, model.TraceStep{Stage: "connectivity", Action: "Unreachable", Decisive: true,
			Note: src.Name + " has no network attachment."})
	}
	return append(steps, bypasses...), decided, vlan
}
