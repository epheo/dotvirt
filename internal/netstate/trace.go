package netstate

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/model"
)

// Trace simulates one flow through the policy planes: the same pure in-memory
// scan Effective does, but rule-level — given a concrete source, destination
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
// Conditional step and downgrades the verdict — never silently dropped.

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

// Trace answers for src → dst (in-cluster) when dst is non-nil, else
// src → dstIP (external).
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

// matched is a rule-component verdict plus why it couldn't be resolved when
// conditional — the reason surfaces on the step so the viewer knows what to
// check, not just that something is uncertain.
type matched struct {
	m      match
	reason string
}

// allOf ANDs components (subject × peer × ports): any miss is a miss, any
// unresolved part makes the whole rule conditional.
func allOf(parts ...matched) matched {
	out := matched{m: matchYes}
	for _, p := range parts {
		if p.m == matchNo {
			return matched{m: matchNo}
		}
		if p.m == matchCond {
			out.m = matchCond
			if out.reason == "" {
				out.reason = p.reason
			}
		}
	}
	return out
}

// anyOf ORs alternatives (a rule's peer list): one hit is a hit; otherwise an
// unresolved alternative keeps the rule possibly matching.
func anyOf(parts ...matched) matched {
	out := matched{m: matchNo}
	for _, p := range parts {
		if p.m == matchYes {
			return p
		}
		if p.m == matchCond && out.m == matchNo {
			out = p
		}
	}
	return out
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

	// Admin tier: every ANP rule in (priority, rule) order. The first match
	// decides — Allow/Deny are final for the flow, Pass hands it down.
anpTier:
	for _, u := range s.sortedANPs() {
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
				addStep(policyFromANP(u, false), rv, "admin", action, "May match: "+m.reason+".", true, false)
				condActions = append(condActions, action)
				continue
			}
			if action == "Pass" {
				addStep(policyFromANP(u, false), rv, "admin", action, "Delegates this flow to the project tier.", false, true)
				break anpTier
			}
			addStep(policyFromANP(u, false), rv, "admin", action, "", false, true)
			return decide(action)
		}
	}

	// A maybe-matching Pass converges here: matched or not, evaluation
	// continues into the tiers below, so it can no longer change the outcome.
	// (Inside the admin tier it still diverges — a later decisive ANP rule
	// would have been skipped by a matching Pass.)
	kept := condActions[:0]
	for _, a := range condActions {
		if a != "Pass" {
			kept = append(kept, a)
		}
	}
	condActions = kept

	// Project tier: rules across every selecting NetworkPolicy are one allow
	// list. Selection alone isolates the direction — no allowing rule means
	// the tier default-denies the flow.
	var selecting []*unstructured.Unstructured
	definiteSel := 0
	for _, u := range listOf(s.netpol) {
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
	for _, u := range listOf(s.banp) {
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
				addStep(policyFromANP(u, true), rv, "baseline", action, "May match: "+m.reason+".", true, false)
				condActions = append(condActions, action)
				continue
			}
			addStep(policyFromANP(u, true), rv, "baseline", action, "", false, true)
			return decide(action)
		}
	}

	steps = append(steps, model.TraceStep{
		Stage: "default", Direction: dir, Action: "Allow", Decisive: true,
		Note: "No policy matches this flow — the network default allows it.",
	})
	return decide("Allow")
}

// gatewayWalk runs the namespace's EgressFirewall rules in order — the
// gateway tier is first-match, default allow.
func (s *Snapshot) gatewayWalk(ns, dstIP, protocol string, port int) walkResult {
	var steps []model.TraceStep
	var condActions []string
	var fw *unstructured.Unstructured
	for _, u := range listOf(s.egressfw) {
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
				// An unrecognized destination form still stays visible — a
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
	var steps []model.TraceStep
	for _, u := range listOf(s.egressip) {
		nsSel, _, _ := unstructured.NestedMap(u.Object, "spec", "namespaceSelector")
		podSel, _, _ := unstructured.NestedMap(u.Object, "spec", "podSelector")
		m := combineMatch(matchSelector(nsSel, src.NSLabels), podMatch(podSel, src.PodLabels, true))
		if m == matchNo {
			continue
		}
		pol := policyFromEgressIP(u)
		var rv *model.PolicyRuleView
		if len(pol.Rules) > 0 {
			rv = &pol.Rules[0]
		}
		pol.Rules = nil
		steps = append(steps, model.TraceStep{Stage: "snat", Policy: &pol, Rule: rv, Action: "SNAT",
			Conditional: m == matchCond, Note: "Egress leaves source-NATed to this pool."})
	}
	for _, u := range listOf(s.extroute) {
		sel, _, _ := unstructured.NestedMap(u.Object, "spec", "from", "namespaceSelector")
		m := matchSelector(sel, src.NSLabels)
		if m == matchNo {
			continue
		}
		pol := policyFromExtRoute(u)
		var rv *model.PolicyRuleView
		if len(pol.Rules) > 0 {
			rv = &pol.Rules[0]
		}
		pol.Rules = nil
		steps = append(steps, model.TraceStep{Stage: "route", Policy: &pol, Rule: rv, Action: "Route",
			Conditional: m == matchCond, Note: "Egress is steered to this next hop instead of the default gateway."})
	}
	return steps
}

// connectivity decides whether an east-west path exists at all: both ends
// must sit on the same primary network — isolated primaries drop the flow
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

// primaryOf resolves the primary network a workload attaches. A default
// multus binding substitutes the named NAD's segment for the pod network, so
// it compares by segment identity, not by the namespace's primary domain;
// otherwise a pod binding resolves the namespace's primary UDN/CUDN, else the
// cluster default. No binding at all means no primary path.
func (s *Snapshot) primaryOf(w TraceWorkload) (key, display string) {
	if w.DefaultNet != "" {
		k, name := s.segmentKey(w.DefaultNet)
		return k, "network " + name
	}
	if !w.PodNet {
		return "", ""
	}
	return s.primaryDomain(w.Namespace)
}

// primaryDomain resolves a namespace's primary network: a Primary-role UDN in
// the namespace, a Primary-role CUDN whose generated NAD sits there, else the
// cluster default network.
func (s *Snapshot) primaryDomain(ns string) (key, display string) {
	for _, u := range listOf(s.udn) {
		if u.GetNamespace() != ns {
			continue
		}
		topology, _, _ := unstructured.NestedString(u.Object, "spec", "topology")
		role, _, _ := unstructured.NestedString(u.Object, "spec", strings.ToLower(topology), "role")
		if strings.EqualFold(role, "Primary") {
			return "udn:" + ns + "/" + u.GetName(), "network " + u.GetName()
		}
	}
	for _, u := range listOf(s.cudn) {
		topology, _, _ := unstructured.NestedString(u.Object, "spec", "network", "topology")
		role, _, _ := unstructured.NestedString(u.Object, "spec", "network", strings.ToLower(topology), "role")
		if !strings.EqualFold(role, "Primary") {
			continue
		}
		for _, nad := range listOf(s.nad) {
			if nad.GetNamespace() != ns {
				continue
			}
			for _, ref := range nad.GetOwnerReferences() {
				if ref.Kind == "ClusterUserDefinedNetwork" && ref.Name == u.GetName() {
					return "cudn:" + u.GetName(), "shared network " + u.GetName()
				}
			}
		}
	}
	return "default", "the cluster default network"
}

// sharedSegments returns the display names of secondary segments both NIC
// sets attach, resolving CUDN-generated NADs to one identity across namespaces.
func (s *Snapshot) sharedSegments(a, b []string) []string {
	seen := map[string]string{}
	for _, ref := range a {
		k, name := s.segmentKey(ref)
		seen[k] = name
	}
	var out []string
	added := map[string]bool{}
	for _, ref := range b {
		k, _ := s.segmentKey(ref)
		if name, ok := seen[k]; ok && !added[k] {
			out = append(out, name)
			added[k] = true
		}
	}
	sort.Strings(out)
	return out
}

// segmentKey resolves one multus ref to a comparable segment identity: NADs
// generated by one CUDN are the same segment in every namespace it spans; a
// UDN-owned or raw NAD is its own.
func (s *Snapshot) segmentKey(ref string) (key, display string) {
	if obj, ok, _ := s.nad.GetByKey(ref); ok {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			for _, r := range u.GetOwnerReferences() {
				switch r.Kind {
				case "ClusterUserDefinedNetwork":
					return "cudn:" + r.Name, r.Name
				case "UserDefinedNetwork":
					return "udn:" + ref, r.Name
				}
			}
		}
	}
	return "nad:" + ref, ref
}

// isLocalnet reports whether a NAD's CNI config declares localnet topology —
// the segment kind with its own path to the physical fabric.
func (s *Snapshot) isLocalnet(ref string) bool {
	obj, ok, _ := s.nad.GetByKey(ref)
	if !ok {
		return false
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return false
	}
	cfg, _, _ := unstructured.NestedString(u.Object, "spec", "config")
	var c struct {
		Topology string `json:"topology"`
	}
	if json.Unmarshal([]byte(cfg), &c) != nil {
		return false
	}
	return strings.EqualFold(c.Topology, "localnet")
}

// sortedANPs returns the admin policies in precedence order (priority, name) —
// the order their rules are evaluated in.
func (s *Snapshot) sortedANPs() []*unstructured.Unstructured {
	anps := listOf(s.anp)
	sort.Slice(anps, func(i, j int) bool {
		pi, _, _ := unstructured.NestedInt64(anps[i].Object, "spec", "priority")
		pj, _, _ := unstructured.NestedInt64(anps[j].Object, "spec", "priority")
		if pi != pj {
			return pi < pj
		}
		return anps[i].GetName() < anps[j].GetName()
	})
	return anps
}

// peerTarget is what rule peers are matched against: the flow's other end —
// an in-cluster workload, or a bare external address.
type peerTarget struct {
	w  *TraceWorkload
	ip string
}

func (t peerTarget) addrs() []string {
	if t.w != nil {
		return t.w.IPs
	}
	return []string{t.ip}
}

// anpPeerMatch evaluates an ANP/BANP rule's peer list against the target.
// Selector peers never match an external address; node peers never match a
// VM's pod-net address, but an external target may itself be a node.
func anpPeerMatch(v any, t peerTarget) matched {
	peers, ok := v.([]any)
	if !ok || len(peers) == 0 {
		return matched{m: matchNo}
	}
	var parts []matched
	for _, raw := range peers {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch {
		case m["namespaces"] != nil:
			if t.w == nil {
				parts = append(parts, matched{m: matchNo})
				continue
			}
			sel, _ := m["namespaces"].(map[string]any)
			parts = append(parts, selMatch(sel, t.w.NSLabels))
		case m["pods"] != nil:
			if t.w == nil {
				parts = append(parts, matched{m: matchNo})
				continue
			}
			pods, _ := m["pods"].(map[string]any)
			nsSel, _ := pods["namespaceSelector"].(map[string]any)
			podSel, _ := pods["podSelector"].(map[string]any)
			parts = append(parts, allOf(selMatch(nsSel, t.w.NSLabels), selMatch(podSel, t.w.PodLabels)))
		case m["networks"] != nil:
			nets, _ := m["networks"].([]any)
			parts = append(parts, cidrsMatch(nets, t))
		case m["nodes"] != nil:
			if t.w == nil {
				parts = append(parts, matched{m: matchCond, reason: "node-peer rule — whether this address is a cluster node is unknown here"})
				continue
			}
			parts = append(parts, matched{m: matchNo})
		}
	}
	return anyOf(parts...)
}

// netpolPeersMatch evaluates a NetworkPolicy rule's from/to list. An empty
// list allows every peer; a bare podSelector means pods in the policy's own
// namespace.
func netpolPeersMatch(v any, t peerTarget, policyNS string) matched {
	peers, ok := v.([]any)
	if !ok || len(peers) == 0 {
		return matched{m: matchYes}
	}
	var parts []matched
	for _, raw := range peers {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if ib, ok := m["ipBlock"].(map[string]any); ok {
			parts = append(parts, ipBlockMatch(ib, t))
			continue
		}
		if t.w == nil {
			parts = append(parts, matched{m: matchNo})
			continue
		}
		nsRaw, nsPresent := m["namespaceSelector"]
		podRaw, podPresent := m["podSelector"]
		nsSel, _ := nsRaw.(map[string]any)
		podSel, _ := podRaw.(map[string]any)
		switch {
		case nsPresent && podPresent:
			parts = append(parts, allOf(selMatch(nsSel, t.w.NSLabels), selMatch(podSel, t.w.PodLabels)))
		case nsPresent:
			parts = append(parts, selMatch(nsSel, t.w.NSLabels))
		case podPresent:
			if t.w.Namespace != policyNS {
				parts = append(parts, matched{m: matchNo})
				continue
			}
			parts = append(parts, selMatch(podSel, t.w.PodLabels))
		}
	}
	return anyOf(parts...)
}

func selMatch(sel map[string]any, lbls map[string]string) matched {
	m := matchSelector(sel, lbls)
	if m == matchCond {
		return matched{m: matchCond, reason: "a selector could not be resolved"}
	}
	return matched{m: m}
}

// cidrsMatch reports whether any target address falls in any CIDR. A workload
// with no reported addresses can't be resolved — conditional, not dropped.
func cidrsMatch(cidrs []any, t peerTarget) matched {
	addrs := t.addrs()
	if len(addrs) == 0 || (len(addrs) == 1 && addrs[0] == "") {
		return matched{m: matchCond, reason: "the VM reports no addresses (not running), so CIDR rules can't be resolved"}
	}
	for _, c := range cidrs {
		pfx, err := netip.ParsePrefix(str(c))
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ad, err := netip.ParseAddr(a)
			if err != nil {
				continue
			}
			if pfx.Contains(ad) {
				return matched{m: matchYes}
			}
		}
	}
	return matched{m: matchNo}
}

// ipBlockMatch is cidrsMatch with the netpol except list: an address inside an
// except block does not match.
func ipBlockMatch(ib map[string]any, t peerTarget) matched {
	addrs := t.addrs()
	if len(addrs) == 0 || (len(addrs) == 1 && addrs[0] == "") {
		return matched{m: matchCond, reason: "the VM reports no addresses (not running), so CIDR rules can't be resolved"}
	}
	pfx, err := netip.ParsePrefix(str(ib["cidr"]))
	if err != nil {
		return matched{m: matchNo}
	}
	excepts, _ := ib["except"].([]any)
	for _, a := range addrs {
		ad, err := netip.ParseAddr(a)
		if err != nil || !pfx.Contains(ad) {
			continue
		}
		excluded := false
		for _, e := range excepts {
			if ep, err := netip.ParsePrefix(str(e)); err == nil && ep.Contains(ad) {
				excluded = true
				break
			}
		}
		if !excluded {
			return matched{m: matchYes}
		}
	}
	return matched{m: matchNo}
}

// netpolPortsMatch evaluates the netpol/EgressFirewall port shape
// {protocol, port, endPort}: absent means all ports; a named port or an
// any-port query against a restricted rule can't be resolved here.
func netpolPortsMatch(v any, protocol string, port int) matched {
	ports, ok := v.([]any)
	if !ok || len(ports) == 0 {
		return matched{m: matchYes}
	}
	var parts []matched
	for _, raw := range ports {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		eproto := str(m["protocol"])
		if eproto == "" {
			eproto = "TCP"
		}
		if !strings.EqualFold(eproto, protocol) {
			parts = append(parts, matched{m: matchNo})
			continue
		}
		pv, present := m["port"]
		if !present {
			parts = append(parts, matched{m: matchYes})
			continue
		}
		if port == 0 {
			parts = append(parts, matched{m: matchCond, reason: "the rule restricts ports; give a destination port to resolve it"})
			continue
		}
		n, isNum := toInt(pv)
		if !isNum {
			parts = append(parts, matched{m: matchCond, reason: "a named port can't be resolved here"})
			continue
		}
		end := n
		if e, ok := toInt(m["endPort"]); ok {
			end = e
		}
		if port >= n && port <= end {
			parts = append(parts, matched{m: matchYes})
		} else {
			parts = append(parts, matched{m: matchNo})
		}
	}
	return anyOf(parts...)
}

// anpPortsMatch evaluates the ANP port shape ({portNumber}, {portRange},
// {namedPort}): absent means all ports.
func anpPortsMatch(v any, protocol string, port int) matched {
	ports, ok := v.([]any)
	if !ok || len(ports) == 0 {
		return matched{m: matchYes}
	}
	var parts []matched
	for _, raw := range ports {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m["namedPort"] != nil {
			parts = append(parts, matched{m: matchCond, reason: "a named port can't be resolved here"})
			continue
		}
		if pn, ok := m["portNumber"].(map[string]any); ok {
			if !strings.EqualFold(str(pn["protocol"]), protocol) {
				parts = append(parts, matched{m: matchNo})
				continue
			}
			if port == 0 {
				parts = append(parts, matched{m: matchCond, reason: "the rule restricts ports; give a destination port to resolve it"})
				continue
			}
			if n, ok := toInt(pn["port"]); ok && n == port {
				parts = append(parts, matched{m: matchYes})
			} else {
				parts = append(parts, matched{m: matchNo})
			}
			continue
		}
		if pr, ok := m["portRange"].(map[string]any); ok {
			if !strings.EqualFold(str(pr["protocol"]), protocol) {
				parts = append(parts, matched{m: matchNo})
				continue
			}
			if port == 0 {
				parts = append(parts, matched{m: matchCond, reason: "the rule restricts ports; give a destination port to resolve it"})
				continue
			}
			start, _ := toInt(pr["start"])
			end, _ := toInt(pr["end"])
			if port >= start && port <= end {
				parts = append(parts, matched{m: matchYes})
			} else {
				parts = append(parts, matched{m: matchNo})
			}
		}
	}
	return anyOf(parts...)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
