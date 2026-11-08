package netstate

// Plain-language sentences for the policy plane: each rule read from its
// subject's side ("Accepts TCP 5432 from VMs labeled app=web in web-prod.")
// and each subject named in words ("VMs labeled app=db in db-prod"). They are
// rendered from the typed rule, never re-parsed from the table summaries, so a
// sentence cannot disagree with what the trace evaluates.

import (
	"strconv"
	"strings"

	"github.com/epheo/dotvirt/internal/netgen"
)

// text renders the rule as one sentence from its subject's side, naming only
// what red lets the caller read (nil names all).
func (r rule) text(dir string, red *Redaction) string {
	peers, ports := r.peersPhrase(red), portsPhrase(r.ports)
	if dir == "Ingress" {
		traffic := orAny(ports, "all traffic")
		switch r.action {
		case "Allow":
			return "Accepts " + traffic + " from " + peers + "."
		case "Deny":
			return "Blocks " + traffic + " from " + peers + "."
		case "Pass":
			return "Leaves " + traffic + " from " + peers + " to project rules."
		}
		return ""
	}
	on := prefixed(" on ", ports)
	switch r.action {
	case "Allow":
		return "Can reach " + peers + on + "."
	case "Deny":
		return "Cannot reach " + peers + on + "."
	case "Pass":
		return "Leaves traffic to " + peers + on + " to project rules."
	}
	return ""
}

// peerText renders the rule from the side of one matched peer: who reaches
// whom when the peer is the network being described. subject names the
// policy's own subject, already rendered for the caller.
func (r rule) peerText(dir string, p peer, subject string, red *Redaction) string {
	who, ports := p.phrase(red), prefixed(" on ", portsPhrase(r.ports))
	if dir == "Ingress" {
		switch r.action {
		case "Allow":
			return capitalize(who) + " can reach " + subject + ports + "."
		case "Deny":
			return capitalize(who) + " cannot reach " + subject + ports + "."
		case "Pass":
			return "Traffic from " + who + " to " + subject + ports + " is left to project rules."
		}
		return ""
	}
	switch r.action {
	case "Allow":
		return capitalize(subject) + " can reach " + who + ports + "."
	case "Deny":
		return capitalize(subject) + " cannot reach " + who + ports + "."
	case "Pass":
		return "Traffic from " + subject + " to " + who + ports + " is left to project rules."
	}
	return ""
}

func (r rule) peersPhrase(red *Redaction) string {
	if len(r.peers) == 0 {
		return "nothing"
	}
	parts := make([]string, 0, len(r.peers))
	for _, p := range r.peers {
		parts = append(parts, p.phrase(red))
	}
	return strings.Join(parts, " or ")
}

func (p peer) phrase(red *Redaction) string {
	switch p.kind {
	case peerAny:
		return "anywhere"
	case peerSelector:
		return vmsPhrase(p.ns, p.pod, p.namespace, red)
	case peerNetworks:
		s := addressesPhrase(p.cidrs)
		if len(p.except) > 0 {
			s += " except " + strings.Join(p.except, ", ")
		}
		return s
	case peerNodes:
		if red.hidesNodes(p.nodes) {
			return selectedLabels + " hosts"
		}
		if l := selectorSummary(p.nodes, nil); l != "" {
			return "hosts labeled " + l
		}
		return "the cluster's hosts"
	case peerCIDRSelector:
		if isAnyCIDR(p.cidrs[0]) {
			return "anything outside"
		}
		return p.cidrs[0]
	case peerDNSName:
		return p.dns
	case peerNodeSelector:
		return "the cluster's hosts"
	}
	return "a destination dotvirt cannot read"
}

// vmsPhrase names the VMs a namespace and pod selector pair picks. own is the
// namespace a selector-free NetworkPolicy peer or subject means (its policy
// is only ever shown to a caller who sees it). Whatever the pair selects
// outside red reads "other namespaces" or "selected".
func vmsPhrase(ns, pod selector, own string, red *Redaction) string {
	who := "VMs"
	switch l := selectorSummary(pod, nil); {
	case red.hidesLabels(pod, ns):
		who = selectedLabels + " VMs"
	case l != "":
		who = "VMs labeled " + l
	}
	switch {
	case own != "" && ns.raw == nil:
		return who + " in " + own
	case emptySelector(ns.raw):
		return who + " in any namespace"
	}
	return who + " in " + namespacesPhrase(ns, red)
}

// namespacesPhrase names the namespaces a non-empty selector picks, as red
// lets the caller read them.
func namespacesPhrase(ns selector, red *Redaction) string {
	if names := netgen.NamespaceNames(ns.raw); names != nil {
		return strings.Join(red.filter(netgen.NamespaceNameLabel, names), ", ")
	}
	if !red.confined(ns) {
		return red.read(ns).own
	}
	return "namespaces labeled " + selectorSummary(ns, red)
}

// applies names a policy's subject in words; ns is the policy's own
// namespace for the namespaced kinds.
func (s subject) applies(ns string, red *Redaction) string {
	if s.none {
		return "nothing"
	}
	if s.ns.raw == nil && ns != "" {
		if emptySelector(s.pod.raw) {
			return "every VM in " + ns
		}
		return vmsPhrase(s.ns, s.pod, ns, red)
	}
	if emptySelector(s.pod.raw) {
		switch {
		case emptySelector(s.ns.raw):
			return "every namespace"
		case netgen.NamespaceNames(s.ns.raw) != nil || !red.confined(s.ns):
			return "every VM in " + namespacesPhrase(s.ns, red)
		}
		return "namespaces labeled " + selectorSummary(s.ns, red)
	}
	return vmsPhrase(s.ns, s.pod, "", red)
}

// isolation states what selecting VMs with a NetworkPolicy does beyond its
// rules: a declared direction admits only what the rules allow.
func (p *policy) isolation() string {
	var out []string
	if p.denyIngress {
		if len(p.ingress) == 0 {
			out = append(out, "Blocks all incoming traffic.")
		} else {
			out = append(out, "Blocks other incoming traffic.")
		}
	}
	if p.denyEgress {
		if len(p.egress) == 0 {
			out = append(out, "Blocks all outgoing traffic.")
		} else {
			out = append(out, "Blocks other outgoing traffic.")
		}
	}
	return strings.Join(out, " ")
}

func addressesPhrase(cidrs []string) string {
	for _, c := range cidrs {
		if isAnyCIDR(c) {
			return "any address"
		}
	}
	return "addresses in " + strings.Join(cidrs, ", ")
}

func isAnyCIDR(c string) bool { return c == "0.0.0.0/0" || c == "::/0" }

// portsPhrase reads the port list as words: "TCP 5432", "UDP 53 and TCP
// 8000-8080". Empty means every port.
func portsPhrase(ports []port) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, p.phrase())
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func (p port) phrase() string {
	switch {
	case p.named != "" && p.protocol == "":
		return "port " + p.named
	case p.named != "":
		return p.protocol + " port " + p.named
	case p.start == 0:
		return "all " + p.protocol
	case p.start == p.end:
		return p.protocol + " " + strconv.Itoa(p.start)
	}
	return p.protocol + " " + strconv.Itoa(p.start) + "-" + strconv.Itoa(p.end)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
