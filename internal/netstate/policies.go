package netstate

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/model"
)

// Policies renders the policy plane from the watch-fed stores — the same pure
// in-memory scan Catalog does for port groups: the DFW tiers (NetworkPolicy,
// AdminNetworkPolicy/Baseline), the per-project Gateway Firewall (EgressFirewall)
// and the Tier-0 planes (EgressIP, external routes). Rules come out as display
// summaries, not spec mirrors: the view reads them, edits go through git. The
// handler scopes namespace-tier rows to visible namespaces and gates the
// cluster-tier rows on authoring authority.
func (s *Snapshot) Policies() []model.Policy {
	out := []model.Policy{}
	for _, u := range listOf(s.netpol) {
		out = append(out, policyFromNetpol(u))
	}
	for _, u := range listOf(s.anp) {
		out = append(out, policyFromANP(u, false))
	}
	for _, u := range listOf(s.banp) {
		out = append(out, policyFromANP(u, true))
	}
	for _, u := range listOf(s.egressfw) {
		out = append(out, policyFromEgressFirewall(u))
	}
	for _, u := range listOf(s.egressip) {
		out = append(out, policyFromEgressIP(u))
	}
	for _, u := range listOf(s.extroute) {
		out = append(out, policyFromExtRoute(u))
	}
	// One deterministic order for every consumer: tier (kind), then ANP
	// precedence, then identity — so the view needs no re-sort and repaints are
	// stable across watch churn.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return kindRank(a.Kind) < kindRank(b.Kind)
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

func kindRank(k model.PolicyKind) int {
	switch k {
	case model.PolicyAdmin:
		return 0
	case model.PolicyBaseline:
		return 1
	case model.PolicyDFW:
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

// policyFromNetpol decodes a NetworkPolicy: the project east-west DFW rules. A
// policy with no rules for a declared direction default-denies it — the view
// derives that hint from kind + empty rules, so nothing is synthesized here.
func policyFromNetpol(u *unstructured.Unstructured) model.Policy {
	p := model.Policy{
		Name:      u.GetName(),
		Kind:      model.PolicyDFW,
		Namespace: u.GetNamespace(),
		Backing:   "NetworkPolicy",
	}
	sel, _, _ := unstructured.NestedMap(u.Object, "spec", "podSelector")
	p.Target = orAny(selectorSummary(sel), "all pods")

	for _, dir := range []struct{ field, label, peerKey string }{
		{"ingress", "Ingress", "from"},
		{"egress", "Egress", "to"},
	} {
		rules, found, _ := unstructured.NestedSlice(u.Object, "spec", dir.field)
		if !found {
			continue
		}
		for _, raw := range rules {
			r, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			p.Rules = append(p.Rules, model.PolicyRuleView{
				Direction: dir.label,
				Action:    "Allow",
				Peer:      netpolPeers(r[dir.peerKey]),
				Ports:     portsSummary(r["ports"]),
			})
		}
	}
	return p
}

// policyFromANP decodes an AdminNetworkPolicy or (baseline=true) the
// BaselineAdminNetworkPolicy — the cluster-wide DFW tiers above/below tenant
// NetworkPolicies.
func policyFromANP(u *unstructured.Unstructured, baseline bool) model.Policy {
	p := model.Policy{
		Name:    u.GetName(),
		Kind:    model.PolicyAdmin,
		Backing: "AdminNetworkPolicy",
	}
	if baseline {
		p.Kind, p.Backing = model.PolicyBaseline, "BaselineAdminNetworkPolicy"
	} else {
		prio, _, _ := unstructured.NestedInt64(u.Object, "spec", "priority")
		p.Priority = int(prio)
	}
	if sel, found, _ := unstructured.NestedMap(u.Object, "spec", "subject", "namespaces"); found {
		p.Target = orAny(selectorSummary(sel), "all namespaces")
		p.Namespaces = selectorNamespaces(sel)
	} else if pods, found, _ := unstructured.NestedMap(u.Object, "spec", "subject", "pods"); found {
		ns, _ := pods["namespaceSelector"].(map[string]any)
		po, _ := pods["podSelector"].(map[string]any)
		p.Target = strings.TrimSpace(orAny(selectorSummary(ns), "all namespaces") + " " + prefixed("pods ", selectorSummary(po)))
		p.Namespaces = selectorNamespaces(ns)
	}

	for _, dir := range []struct{ field, label, peerKey string }{
		{"ingress", "Ingress", "from"},
		{"egress", "Egress", "to"},
	} {
		rules, found, _ := unstructured.NestedSlice(u.Object, "spec", dir.field)
		if !found {
			continue
		}
		for _, raw := range rules {
			r, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			p.Rules = append(p.Rules, model.PolicyRuleView{
				Direction: dir.label,
				Action:    str(r["action"]),
				Peer:      adminPeers(r[dir.peerKey]),
				Ports:     portsSummary(r["ports"]),
			})
		}
	}
	return p
}

// policyFromEgressFirewall decodes a namespace's EgressFirewall — the Tier-1
// gateway firewall: ordered first-match allow/deny against external destinations.
func policyFromEgressFirewall(u *unstructured.Unstructured) model.Policy {
	p := model.Policy{
		Name:      u.GetName(),
		Kind:      model.PolicyGateway,
		Namespace: u.GetNamespace(),
		Backing:   "EgressFirewall",
		Target:    "all pods",
	}
	rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
	for _, raw := range rules {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		to, _ := r["to"].(map[string]any)
		peer := str(to["cidrSelector"])
		if peer == "" {
			peer = str(to["dnsName"])
		}
		if peer == "" && to["nodeSelector"] != nil {
			peer = "cluster nodes"
		}
		p.Rules = append(p.Rules, model.PolicyRuleView{
			Direction: "Egress",
			Action:    str(r["type"]),
			Peer:      peer,
			Ports:     portsSummary(r["ports"]),
		})
	}
	return p
}

// policyFromEgressIP decodes a cluster-scoped EgressIP — the Tier-0 SNAT pool.
// The one rule row carries the pool; Target is the namespaces it pins.
func policyFromEgressIP(u *unstructured.Unstructured) model.Policy {
	p := model.Policy{
		Name:    u.GetName(),
		Kind:    model.PolicyEgressIP,
		Backing: "EgressIP",
	}
	sel, _, _ := unstructured.NestedMap(u.Object, "spec", "namespaceSelector")
	p.Target = orAny(selectorSummary(sel), "all namespaces")
	p.Namespaces = selectorNamespaces(sel)
	ips, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "egressIPs")
	if len(ips) > 0 {
		p.Rules = []model.PolicyRuleView{{Direction: "Egress", Action: "SNAT", Peer: strings.Join(ips, ", ")}}
	}
	return p
}

// policyFromExtRoute decodes a cluster-scoped AdminPolicyBasedExternalRoute —
// the Tier-0 static next-hop route for the selected projects' egress.
func policyFromExtRoute(u *unstructured.Unstructured) model.Policy {
	p := model.Policy{
		Name:    u.GetName(),
		Kind:    model.PolicyRoute,
		Backing: "AdminPolicyBasedExternalRoute",
	}
	sel, _, _ := unstructured.NestedMap(u.Object, "spec", "from", "namespaceSelector")
	p.Target = orAny(selectorSummary(sel), "all namespaces")
	p.Namespaces = selectorNamespaces(sel)
	hops, _, _ := unstructured.NestedSlice(u.Object, "spec", "nextHops", "static")
	var ips []string
	for _, raw := range hops {
		if h, ok := raw.(map[string]any); ok {
			if ip := str(h["ip"]); ip != "" {
				ips = append(ips, ip)
			}
		}
	}
	if len(ips) > 0 {
		p.Rules = []model.PolicyRuleView{{Direction: "Egress", Action: "Route", Peer: "via " + strings.Join(ips, ", ")}}
	}
	return p
}

// netpolPeers summarizes a NetworkPolicy rule's from/to list: each peer is a
// pod/namespace selector pair or an ipBlock; peers join with "; ".
func netpolPeers(v any) string {
	peers, ok := v.([]any)
	if !ok || len(peers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(peers))
	for _, raw := range peers {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var b []string
		if ns, ok := m["namespaceSelector"].(map[string]any); ok {
			b = append(b, "ns "+orAny(selectorSummary(ns), "any"))
		}
		if po, ok := m["podSelector"].(map[string]any); ok {
			b = append(b, "pods "+orAny(selectorSummary(po), "any"))
		}
		if ip, ok := m["ipBlock"].(map[string]any); ok {
			b = append(b, "cidr "+str(ip["cidr"]))
		}
		if len(b) > 0 {
			parts = append(parts, strings.Join(b, " "))
		}
	}
	return strings.Join(parts, "; ")
}

// adminPeers summarizes an ANP/BANP rule's peer list: namespace Groups, pod
// Groups, or (egress) raw networks CIDRs.
func adminPeers(v any) string {
	peers, ok := v.([]any)
	if !ok || len(peers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(peers))
	for _, raw := range peers {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch {
		case m["namespaces"] != nil:
			ns, _ := m["namespaces"].(map[string]any)
			parts = append(parts, "ns "+orAny(selectorSummary(ns), "any"))
		case m["pods"] != nil:
			pods, _ := m["pods"].(map[string]any)
			ns, _ := pods["namespaceSelector"].(map[string]any)
			po, _ := pods["podSelector"].(map[string]any)
			parts = append(parts, strings.TrimSpace("ns "+orAny(selectorSummary(ns), "any")+" "+prefixed("pods ", selectorSummary(po))))
		case m["networks"] != nil:
			if nets, ok := m["networks"].([]any); ok {
				cidrs := make([]string, 0, len(nets))
				for _, n := range nets {
					cidrs = append(cidrs, str(n))
				}
				parts = append(parts, "cidr "+strings.Join(cidrs, ", "))
			}
		case m["nodes"] != nil:
			nodes, _ := m["nodes"].(map[string]any)
			parts = append(parts, "nodes "+orAny(selectorSummary(nodes), "any"))
		}
	}
	return strings.Join(parts, "; ")
}

// selectorSummary renders a LabelSelector for humans: "k=v, k2=v2" plus
// "key op (values)" expressions. The name-In expression netgen writes for
// namespace pickers collapses to the bare namespace list. Empty selector = "".
func selectorSummary(sel map[string]any) string {
	if len(sel) == 0 {
		return ""
	}
	var parts []string
	if ml, ok := sel["matchLabels"].(map[string]any); ok {
		keys := make([]string, 0, len(ml))
		for k := range ml {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, k+"="+str(ml[k]))
		}
	}
	if mes, ok := sel["matchExpressions"].([]any); ok {
		for _, raw := range mes {
			me, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			var vals []string
			if vs, ok := me["values"].([]any); ok {
				for _, v := range vs {
					vals = append(vals, str(v))
				}
			}
			if str(me["key"]) == "kubernetes.io/metadata.name" && str(me["operator"]) == "In" {
				parts = append(parts, strings.Join(vals, ", "))
				continue
			}
			s := str(me["key"]) + " " + strings.ToLower(str(me["operator"]))
			if len(vals) > 0 {
				s += " (" + strings.Join(vals, ", ") + ")"
			}
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

// selectorNamespaces enumerates the namespaces a selector provably pins to:
// the metadata.name In expression netgen writes, or a bare metadata.name
// matchLabel. Any other selector returns nil — label-based membership can't be
// evaluated here, and a tenant filter must not hide a possibly-applying row.
func selectorNamespaces(sel map[string]any) []string {
	if len(sel) == 0 {
		return nil
	}
	ml, _ := sel["matchLabels"].(map[string]any)
	mes, _ := sel["matchExpressions"].([]any)
	if len(ml) == 1 && len(mes) == 0 {
		if name := str(ml["kubernetes.io/metadata.name"]); name != "" {
			return []string{name}
		}
		return nil
	}
	if len(ml) == 0 && len(mes) == 1 {
		me, ok := mes[0].(map[string]any)
		if !ok || str(me["key"]) != "kubernetes.io/metadata.name" || str(me["operator"]) != "In" {
			return nil
		}
		vs, _ := me["values"].([]any)
		out := make([]string, 0, len(vs))
		for _, v := range vs {
			if s := str(v); s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return nil
}

// portsSummary renders a rule's port list compactly. It reads all three shapes
// the plane carries: netpol/EgressFirewall {protocol, port, endPort}, ANP
// {portNumber: {...}} and {portRange: {protocol, start, end}}.
func portsSummary(v any) string {
	ports, ok := v.([]any)
	if !ok || len(ports) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ports))
	for _, raw := range ports {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if pn, ok := m["portNumber"].(map[string]any); ok {
			m = pn
		} else if pr, ok := m["portRange"].(map[string]any); ok {
			parts = append(parts, fmt.Sprintf("%s/%s-%s", str(pr["protocol"]), num(pr["start"]), num(pr["end"])))
			continue
		}
		p := str(m["protocol"]) + "/" + num(m["port"])
		if end, ok := toInt(m["endPort"]); ok {
			p += "-" + fmt.Sprint(end)
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

// num renders an int-or-string port value (a netpol port may be a named port).
func num(v any) string {
	if n, ok := toInt(v); ok {
		return fmt.Sprint(n)
	}
	return str(v)
}

func orAny(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// prefixed returns prefix+s, or "" when s is empty — for optional summary parts.
func prefixed(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}
