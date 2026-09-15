package netstate

import (
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
)

// rule is one policy rule decoded once for every reader: the action, the
// peers the flow's other end is matched against, and the port constraints.
// view renders it for the Security tables; match (tracematch.go) evaluates it
// in the trace, so the two can never read a shape differently.
type rule struct {
	action string
	peers  []peer
	ports  []port
}

type peerKind int

const (
	peerAny          peerKind = iota // a NetworkPolicy rule with no from/to: every peer
	peerSelector                     // namespace and/or pod label selectors
	peerNetworks                     // ANP networks or NetworkPolicy ipBlock: CIDRs with exceptions
	peerNodes                        // ANP nodes selector
	peerCIDRSelector                 // EgressFirewall cidrSelector: one destination CIDR
	peerDNSName                      // EgressFirewall dnsName
	peerNodeSelector                 // EgressFirewall nodeSelector: the cluster's nodes
	peerUnresolved                   // an EgressFirewall destination form this plane cannot read
)

// peer is one alternative in a rule's peer list. nil selectors are
// unconstrained; namespace is set for a NetworkPolicy podSelector written
// without a namespaceSelector, which means the policy's own namespace.
type peer struct {
	kind      peerKind
	ns, pod   *metav1.LabelSelector
	nodes     *metav1.LabelSelector
	namespace string
	cidrs     []string
	except    []string
	dns       string
}

// port is one port constraint in the form every kind shares: a protocol and
// either every port of it, one inclusive numeric range, or a named port.
type port struct {
	protocol   string // empty only for an admin namedPort, which carries none
	start, end int    // 0: every port of the protocol
	named      string
}

// netpolRule reads a NetworkPolicy rule. An empty peer list allows every
// peer; an ipBlock peer carries no selectors.
func netpolRule(peers []netgen.NetworkPolicyPeer, ports []netgen.PortDoc, ns string) rule {
	r := rule{action: "Allow", ports: decodePorts(ports, "TCP")}
	if len(peers) == 0 {
		r.peers = []peer{{kind: peerAny}}
		return r
	}
	for _, p := range peers {
		switch {
		case p.IPBlock != nil:
			r.peers = append(r.peers, peer{kind: peerNetworks, cidrs: []string{p.IPBlock.CIDR}, except: p.IPBlock.Except})
		case p.NamespaceSelector != nil || p.PodSelector != nil:
			sel := peer{kind: peerSelector, ns: p.NamespaceSelector, pod: p.PodSelector}
			if p.NamespaceSelector == nil {
				sel.namespace = ns
			}
			r.peers = append(r.peers, sel)
		}
	}
	return r
}

// adminRule reads an ANP/BANP rule: namespace Groups, pod Groups, raw
// networks CIDRs or node selectors. An empty peer list matches nothing.
func adminRule(action string, peers []netgen.AdminPeerDoc, ports []netgen.PortDoc) rule {
	r := rule{action: action, ports: decodePorts(ports, "")}
	for _, p := range peers {
		switch {
		case p.Namespaces != nil:
			r.peers = append(r.peers, peer{kind: peerSelector, ns: p.Namespaces})
		case p.Pods != nil:
			// The API requires both selectors, so an empty podSelector is no
			// narrowing at all and reads as unconstrained, not as "pods any".
			sel := peer{kind: peerSelector, ns: &p.Pods.NamespaceSelector}
			if !emptySelector(&p.Pods.PodSelector) {
				sel.pod = &p.Pods.PodSelector
			}
			r.peers = append(r.peers, sel)
		case p.Networks != nil:
			r.peers = append(r.peers, peer{kind: peerNetworks, cidrs: p.Networks})
		case p.Nodes != nil:
			r.peers = append(r.peers, peer{kind: peerNodes, nodes: p.Nodes})
		}
	}
	return r
}

// gatewayRule reads an EgressFirewall rule: one destination, first-match.
func gatewayRule(r netgen.EgressFirewallRuleDoc) rule {
	var p peer
	switch to := r.To; {
	case to.CIDRSelector != "":
		p = peer{kind: peerCIDRSelector, cidrs: []string{to.CIDRSelector}}
	case to.DNSName != "":
		p = peer{kind: peerDNSName, dns: to.DNSName}
	case to.NodeSelector != nil:
		p = peer{kind: peerNodeSelector, nodes: to.NodeSelector}
	default:
		// An unrecognized destination stays a visible, unresolvable peer:
		// dropping the rule would let a later one decide with false certainty.
		p = peer{kind: peerUnresolved}
	}
	return rule{action: r.Type, peers: []peer{p}, ports: decodePorts(r.Ports, "TCP")}
}

// decodePorts flattens the wire forms into port. defaultProto stands in for
// an absent protocol where the API defaults it (NetworkPolicy: TCP).
func decodePorts(docs []netgen.PortDoc, defaultProto string) []port {
	var out []port
	for _, d := range docs {
		switch {
		case d.NamedPort != nil:
			out = append(out, port{named: *d.NamedPort})
		case d.PortRange != nil:
			out = append(out, port{protocol: d.PortRange.Protocol, start: d.PortRange.Start, end: d.PortRange.End})
		default:
			if d.PortNumber != nil {
				d = *d.PortNumber
			}
			p := port{protocol: d.Protocol}
			if p.protocol == "" {
				p.protocol = defaultProto
			}
			switch {
			case d.Port == nil:
			case d.Port.Type == intstr.String:
				p.named = d.Port.StrVal
			default:
				p.start, p.end = d.Port.IntValue(), d.Port.IntValue()
				if d.EndPort != 0 {
					p.end = d.EndPort
				}
			}
			out = append(out, p)
		}
	}
	return out
}

// view renders the rule as one Security table row.
func (r rule) view(dir string) model.PolicyRuleView {
	return model.PolicyRuleView{Direction: dir, Action: r.action, Peer: r.peerSummary(), Ports: r.portSummary()}
}

func (r rule) peerSummary() string {
	var parts []string
	for _, p := range r.peers {
		if s := p.summary(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "; ")
}

func (r rule) portSummary() string {
	parts := make([]string, 0, len(r.ports))
	for _, p := range r.ports {
		parts = append(parts, p.summary())
	}
	return strings.Join(parts, ", ")
}

func (p peer) summary() string {
	switch p.kind {
	case peerSelector:
		var parts []string
		if p.ns != nil {
			parts = append(parts, "ns "+orAny(selectorSummary(p.ns), "any"))
		}
		if p.pod != nil {
			parts = append(parts, "pods "+orAny(selectorSummary(p.pod), "any"))
		}
		return strings.Join(parts, " ")
	case peerNetworks:
		return "cidr " + strings.Join(p.cidrs, ", ")
	case peerNodes:
		return "nodes " + orAny(selectorSummary(p.nodes), "any")
	case peerCIDRSelector:
		return p.cidrs[0]
	case peerDNSName:
		return p.dns
	case peerNodeSelector:
		return "cluster nodes"
	case peerUnresolved:
		return "unresolved destination"
	}
	return ""
}

func (p port) summary() string {
	s := p.protocol
	switch {
	case p.named != "":
		s += "/" + p.named
	case p.start == 0:
	case p.start == p.end:
		s += "/" + strconv.Itoa(p.start)
	default:
		s += "/" + strconv.Itoa(p.start) + "-" + strconv.Itoa(p.end)
	}
	return strings.TrimPrefix(s, "/")
}
