package netstate

// The trace's rule matchers: the three-valued verdict (yes / no /
// conditional-with-reason) and rule.match over the typed rule, so an
// unresolvable rule stays visible instead of silently dropping.

import (
	"fmt"
	"net/netip"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// Conditional-outcome reasons. Each situation is worded here once, so the
// trace UI never shows two phrasings for one condition.
const (
	reasonPortNeeded = "the rule restricts ports; give a destination port to resolve it"
	reasonNamedPort  = "a named port can't be resolved here"
	reasonNoAddrs    = "the VM reports no addresses (not running), so CIDR rules can't be resolved"
	reasonSelector   = "a selector could not be resolved"
	reasonSubject    = "the policy's subject selector could not be resolved"
	reasonNodePeer   = "a node-selector rule - whether this address is a cluster node is unknown here"
	reasonDNSName    = "DNS-name rule (%s) - resolution unknown here"
	reasonUnresolved = "a destination form this trace cannot resolve"
)

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

// match evaluates the rule against the flow's other end and the queried
// protocol/port: any peer alternative may match, and the ports must.
func (r rule) match(t peerTarget, protocol string, port int) matched {
	peers := make([]matched, 0, len(r.peers))
	for _, p := range r.peers {
		peers = append(peers, p.match(t))
	}
	ports := matched{m: matchYes}
	if len(r.ports) > 0 {
		parts := make([]matched, 0, len(r.ports))
		for _, p := range r.ports {
			parts = append(parts, p.match(protocol, port))
		}
		ports = anyOf(parts...)
	}
	return allOf(anyOf(peers...), ports)
}

// match evaluates one peer. Selector peers never match an external address;
// node peers never match a VM's pod-net address, but an external target may
// itself be a node.
func (p peer) match(t peerTarget) matched {
	switch p.kind {
	case peerAny:
		return matched{m: matchYes}
	case peerSelector:
		if t.w == nil || (p.namespace != "" && t.w.Namespace != p.namespace) {
			return matched{m: matchNo}
		}
		return allOf(selMatch(p.ns, t.w.NSLabels), selMatch(p.pod, t.w.PodLabels))
	case peerNetworks, peerCIDRSelector:
		return cidrsMatch(p.cidrs, p.except, t)
	case peerNodes, peerNodeSelector:
		if t.w != nil {
			return matched{m: matchNo}
		}
		return matched{m: matchCond, reason: reasonNodePeer}
	case peerDNSName:
		return matched{m: matchCond, reason: fmt.Sprintf(reasonDNSName, p.dns)}
	}
	return matched{m: matchCond, reason: reasonUnresolved}
}

func selMatch(sel *metav1.LabelSelector, lbls map[string]string) matched {
	m := matchSelector(sel, lbls)
	if m == matchCond {
		return matched{m: matchCond, reason: reasonSelector}
	}
	return matched{m: m}
}

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

// cidrsMatch reports whether any target address falls in any CIDR without
// falling in an except block. A workload with no reported addresses can't be
// resolved - conditional, not dropped.
func cidrsMatch(cidrs, except []string, t peerTarget) matched {
	addrs, cond := resolveAddrs(t)
	if cond != nil {
		return *cond
	}
	for _, c := range cidrs {
		pfx, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		for _, ad := range addrs {
			if pfx.Contains(ad) && !excepted(ad, except) {
				return matched{m: matchYes}
			}
		}
	}
	return matched{m: matchNo}
}

func excepted(ad netip.Addr, except []string) bool {
	for _, e := range except {
		if ep, err := netip.ParsePrefix(e); err == nil && ep.Contains(ad) {
			return true
		}
	}
	return false
}

// match resolves the constraint against the queried (protocol, port); port 0
// means none was given, which only an any-port rule can answer.
func (p port) match(protocol string, port int) matched {
	if p.protocol != "" && !strings.EqualFold(p.protocol, protocol) {
		return matched{m: matchNo}
	}
	if p.named != "" {
		return matched{m: matchCond, reason: reasonNamedPort}
	}
	if p.start == 0 {
		return matched{m: matchYes}
	}
	if port == 0 {
		return matched{m: matchCond, reason: reasonPortNeeded}
	}
	if port >= p.start && port <= p.end {
		return matched{m: matchYes}
	}
	return matched{m: matchNo}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
