package netstate

// The trace's rule matchers: peer/port/CIDR evaluation with the three-valued
// matched verdict (yes / no / conditional-with-reason), so an unresolvable
// rule stays visible instead of silently dropping.

import (
	"net/netip"
	"sort"
	"strings"

	"github.com/epheo/dotvirt/internal/reflect"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// matched is a rule-component verdict plus why it couldn't be resolved when
// conditional - the reason surfaces on the step so the viewer knows what to
// check, not just that something is uncertain.
type matched struct {
	m      match
	reason string
}

// allOf ANDs components (subject x peer x ports): any miss is a miss, any
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

// sortedANPs returns the admin policies in precedence order (priority, name) -
// the order their rules are evaluated in.
func (s *Snapshot) sortedANPs() []*unstructured.Unstructured {
	anps := reflect.List(s.anp)
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

// peerTarget is what rule peers are matched against: the flow's other end -
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

// Conditional-outcome reasons shared by the port and CIDR matchers; every
// producer of one of these situations must word it identically or the trace
// UI shows two phrasings for one condition.
const (
	reasonPortNeeded = "the rule restricts ports; give a destination port to resolve it"
	reasonNamedPort  = "a named port can't be resolved here"
	reasonNoAddrs    = "the VM reports no addresses (not running), so CIDR rules can't be resolved"
)

// resolveAddrs returns the target's parseable addresses, or the conditional
// match when the workload reports none (not running).
func resolveAddrs(t peerTarget) ([]netip.Addr, *matched) {
	raw := t.addrs()
	if len(raw) == 0 || (len(raw) == 1 && raw[0] == "") {
		return nil, &matched{m: matchCond, reason: reasonNoAddrs}
	}
	var addrs []netip.Addr
	for _, a := range raw {
		if ad, err := netip.ParseAddr(a); err == nil {
			addrs = append(addrs, ad)
		}
	}
	return addrs, nil
}

// cidrsMatch reports whether any target address falls in any CIDR. A workload
// with no reported addresses can't be resolved - conditional, not dropped.
func cidrsMatch(cidrs []any, t peerTarget) matched {
	addrs, cond := resolveAddrs(t)
	if cond != nil {
		return *cond
	}
	for _, c := range cidrs {
		pfx, err := netip.ParsePrefix(str(c))
		if err != nil {
			continue
		}
		for _, ad := range addrs {
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
	addrs, cond := resolveAddrs(t)
	if cond != nil {
		return *cond
	}
	pfx, err := netip.ParsePrefix(str(ib["cidr"]))
	if err != nil {
		return matched{m: matchNo}
	}
	excepts, _ := ib["except"].([]any)
	for _, ad := range addrs {
		if !pfx.Contains(ad) {
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

// rangeMatch resolves a numeric proto + inclusive-range port constraint
// against the queried (protocol, port); port 0 means none was given.
func rangeMatch(eproto, protocol string, start, end, port int) matched {
	if !strings.EqualFold(eproto, protocol) {
		return matched{m: matchNo}
	}
	if port == 0 {
		return matched{m: matchCond, reason: reasonPortNeeded}
	}
	if port >= start && port <= end {
		return matched{m: matchYes}
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
			parts = append(parts, matched{m: matchCond, reason: reasonPortNeeded})
			continue
		}
		n, isNum := toInt(pv)
		if !isNum {
			parts = append(parts, matched{m: matchCond, reason: reasonNamedPort})
			continue
		}
		end := n
		if e, ok := toInt(m["endPort"]); ok {
			end = e
		}
		parts = append(parts, rangeMatch(eproto, protocol, n, end, port))
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
			parts = append(parts, matched{m: matchCond, reason: reasonNamedPort})
			continue
		}
		if pn, ok := m["portNumber"].(map[string]any); ok {
			n, _ := toInt(pn["port"])
			parts = append(parts, rangeMatch(str(pn["protocol"]), protocol, n, n, port))
			continue
		}
		if pr, ok := m["portRange"].(map[string]any); ok {
			start, _ := toInt(pr["start"])
			end, _ := toInt(pr["end"])
			parts = append(parts, rangeMatch(str(pr["protocol"]), protocol, start, end, port))
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
