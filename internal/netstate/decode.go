package netstate

// One decoder per policy kind: each live object converts once into its
// netgen document shape and from there into a policy, the row the views show
// plus the typed subject and rules the matchers evaluate. Policies, Effective
// and Trace all read through here, so no reader can walk a shape differently.

import (
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/reflect"
)

// decode converts a live object into its document shape. A conversion error
// is not fatal: the apiserver validated the object, so a field that fails to
// convert is near impossible, and the fields converted before it still keep
// the row visible rather than dropping a possibly-applying object.
func decode[T any](u *unstructured.Unstructured) T {
	var doc T
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &doc)
	return doc
}

type policy struct {
	view    model.Policy
	subject subject
	ingress []rule
	egress  []rule
	// NetworkPolicy only: the directions a definite selection default-denies.
	denyIngress, denyEgress bool
}

func (p *policy) rules(dir string) []rule {
	if dir == "Ingress" {
		return p.ingress
	}
	return p.egress
}

// finish fills the columns derived from the subject and rules.
func (p *policy) finish() *policy {
	p.view.Target = p.subject.summary()
	for _, r := range p.ingress {
		p.view.Rules = append(p.view.Rules, r.view("Ingress"))
	}
	for _, r := range p.egress {
		p.view.Rules = append(p.view.Rules, r.view("Egress"))
	}
	return p
}

// subject is what a policy applies to. nil selectors are unconstrained: a
// NetworkPolicy carries only a pod selector (its namespace is the caller's
// check), the cluster kinds a namespace selector and, for an admin pods
// subject or an EgressIP, a pod selector narrowing it.
type subject struct {
	ns, pod *metav1.LabelSelector
	none    bool // an admin policy without a subject applies to nothing
}

func (s subject) summary() string {
	if s.none {
		return ""
	}
	if s.ns == nil {
		return orAny(selectorSummary(s.pod), "all pods")
	}
	return strings.TrimSpace(orAny(selectorSummary(s.ns), "all namespaces") + " " + prefixed("pods ", selectorSummary(s.pod)))
}

// decodeNetpol reads a NetworkPolicy: the project east-west DFW rules. A
// policy with no rules for a declared direction default-denies it - the view
// derives that hint from kind + empty rules, so nothing is synthesized here.
func decodeNetpol(u *unstructured.Unstructured) *policy {
	doc := decode[netgen.NetworkPolicyDoc](u)
	ns := u.GetNamespace()
	p := &policy{
		view:    model.Policy{Name: u.GetName(), Kind: model.PolicyDFW, Namespace: ns, Backing: "NetworkPolicy"},
		subject: subject{pod: &doc.Spec.PodSelector},
	}
	p.denyIngress, p.denyEgress = netpolTypes(doc)
	for _, r := range doc.Spec.Ingress {
		p.ingress = append(p.ingress, netpolRule(r.From, r.Ports, ns))
	}
	for _, r := range doc.Spec.Egress {
		p.egress = append(p.egress, netpolRule(r.To, r.Ports, ns))
	}
	return p.finish()
}

// netpolTypes reports which directions a NetworkPolicy default-denies for the
// pods it selects, honoring the API defaulting: policyTypes absent means
// Ingress, plus Egress when an egress section is present, even an empty one.
func netpolTypes(doc netgen.NetworkPolicyDoc) (ingress, egress bool) {
	if types := doc.Spec.PolicyTypes; len(types) > 0 {
		for _, t := range types {
			ingress = ingress || t == "Ingress"
			egress = egress || t == "Egress"
		}
		return ingress, egress
	}
	return true, doc.Spec.Egress != nil
}

func decodeANP(u *unstructured.Unstructured) *policy  { return decodeAdmin(u, false) }
func decodeBANP(u *unstructured.Unstructured) *policy { return decodeAdmin(u, true) }

// decodeAdmin reads an AdminNetworkPolicy or (baseline) the
// BaselineAdminNetworkPolicy - the cluster-wide DFW tiers above/below tenant
// NetworkPolicies.
func decodeAdmin(u *unstructured.Unstructured, baseline bool) *policy {
	doc := decode[netgen.AdminNetworkPolicyDoc](u)
	p := &policy{view: model.Policy{Name: u.GetName(), Kind: model.PolicyAdmin, Backing: "AdminNetworkPolicy"}}
	if baseline {
		p.view.Kind, p.view.Backing = model.PolicyBaseline, "BaselineAdminNetworkPolicy"
	} else {
		p.view.Priority = doc.Spec.Priority
	}
	switch sub := doc.Spec.Subject; {
	case sub.Namespaces != nil:
		p.subject = subject{ns: sub.Namespaces}
	case sub.Pods != nil:
		p.subject = subject{ns: &sub.Pods.NamespaceSelector, pod: &sub.Pods.PodSelector}
	default:
		p.subject = subject{none: true}
	}
	p.view.Namespaces = netgen.NamespaceNames(p.subject.ns)
	for _, r := range doc.Spec.Ingress {
		p.ingress = append(p.ingress, adminRule(r.Action, r.From, r.Ports))
	}
	for _, r := range doc.Spec.Egress {
		p.egress = append(p.egress, adminRule(r.Action, r.To, r.Ports))
	}
	return p.finish()
}

// decodeGateway reads a namespace's EgressFirewall - the Tier-1 gateway
// firewall: ordered first-match allow/deny against external destinations.
func decodeGateway(u *unstructured.Unstructured) *policy {
	doc := decode[netgen.EgressFirewallDoc](u)
	p := &policy{view: model.Policy{Name: u.GetName(), Kind: model.PolicyGateway, Namespace: u.GetNamespace(), Backing: "EgressFirewall"}}
	for _, r := range doc.Spec.Egress {
		p.egress = append(p.egress, gatewayRule(r))
	}
	return p.finish()
}

// decodeEgressIP reads a cluster-scoped EgressIP - the Tier-0 SNAT pool. The
// one rule row carries the pool; the subject is the namespaces it pins.
func decodeEgressIP(u *unstructured.Unstructured) *policy {
	doc := decode[netgen.EgressIPDoc](u)
	p := &policy{
		view:    model.Policy{Name: u.GetName(), Kind: model.PolicyEgressIP, Backing: "EgressIP"},
		subject: subject{ns: &doc.Spec.NamespaceSelector, pod: &doc.Spec.PodSelector},
	}
	p.view.Namespaces = netgen.NamespaceNames(p.subject.ns)
	if len(doc.Spec.EgressIPs) > 0 {
		p.view.Rules = []model.PolicyRuleView{{Direction: "Egress", Action: "SNAT", Peer: strings.Join(doc.Spec.EgressIPs, ", ")}}
	}
	return p.finish()
}

// decodeExtRoute reads a cluster-scoped AdminPolicyBasedExternalRoute - the
// Tier-0 static next-hop route for the selected projects' egress.
func decodeExtRoute(u *unstructured.Unstructured) *policy {
	doc := decode[netgen.ExternalRouteDoc](u)
	p := &policy{
		view:    model.Policy{Name: u.GetName(), Kind: model.PolicyRoute, Backing: "AdminPolicyBasedExternalRoute"},
		subject: subject{ns: &doc.Spec.From.NamespaceSelector},
	}
	p.view.Namespaces = netgen.NamespaceNames(p.subject.ns)
	var ips []string
	for _, h := range doc.Spec.NextHops.Static {
		if h.IP != "" {
			ips = append(ips, h.IP)
		}
	}
	if len(ips) > 0 {
		p.view.Rules = []model.PolicyRuleView{{Direction: "Egress", Action: "Route", Peer: "via " + strings.Join(ips, ", ")}}
	}
	return p.finish()
}

type decoder func(*unstructured.Unstructured) *policy

// decoded runs one store's objects through a kind's decoder.
func decoded(idx cache.Indexer, dec decoder) []*policy {
	objs := reflect.List(idx)
	out := make([]*policy, 0, len(objs))
	for _, u := range objs {
		out = append(out, dec(u))
	}
	return out
}

// inNamespace decodes a namespaced store's objects for ns, by name, so a
// tier's walk order does not follow the store's hash order.
func inNamespace(idx cache.Indexer, ns string, dec decoder) []*policy {
	var out []*policy
	for _, u := range reflect.List(idx) {
		if u.GetNamespace() == ns {
			out = append(out, dec(u))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].view.Name < out[j].view.Name })
	return out
}

// sortedANPs returns the admin policies in precedence order (priority, name):
// the order Trace evaluates them in and Effective lists them in.
func (s *Snapshot) sortedANPs() []*policy {
	ps := decoded(s.anp, decodeANP)
	sort.Slice(ps, func(i, j int) bool {
		a, b := ps[i].view, ps[j].view
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.Name < b.Name
	})
	return ps
}

func (s *Snapshot) baselines() []*policy { return decoded(s.banp, decodeBANP) }

// gateways returns the namespace's EgressFirewalls (OVN-K allows one).
func (s *Snapshot) gateways(ns string) []*policy { return inNamespace(s.egressfw, ns, decodeGateway) }
