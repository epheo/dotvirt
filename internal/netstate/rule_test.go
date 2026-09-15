package netstate

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// netpolPeer, anpPeer and gatewayRule decode one rule through the real kind
// decoders, so the table below exercises the same path the stores feed.
func netpolPeer(peers ...map[string]any) rule {
	from := make([]any, 0, len(peers))
	for _, p := range peers {
		from = append(from, p)
	}
	p := decodeNetpol(&unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "r", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"ingress":     []any{map[string]any{"from": from}},
		},
	}})
	return p.ingress[0]
}

func anpPeer(peer map[string]any) rule {
	p := decodeANP(&unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "r"},
		"spec": map[string]any{
			"subject": map[string]any{"namespaces": map[string]any{}},
			"egress":  []any{map[string]any{"action": "Allow", "to": []any{peer}}},
		},
	}})
	return p.egress[0]
}

func gatewayPeer(to map[string]any) rule {
	p := decodeGateway(&unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "default", "namespace": "team-a"},
		"spec":     map[string]any{"egress": []any{map[string]any{"type": "Allow", "to": to}}},
	}})
	return p.egress[0]
}

// Every peer and port form the plane reads goes through the one typed rule:
// the Security table's summary and the trace's verdict come from the same
// decoding, so a shape can never read one way in a table and another in a
// trace.
func TestRuleForms(t *testing.T) {
	prod := map[string]any{"matchLabels": map[string]any{"env": "prod"}}
	web := map[string]any{"matchLabels": map[string]any{"app": "web"}}
	nsLabels := map[string]string{"env": "prod"}
	podLabels := map[string]string{"app": "web"}
	vm := peerTarget{w: &TraceWorkload{Namespace: "team-a", NSLabels: nsLabels, PodLabels: podLabels, IPs: []string{"10.128.2.5"}}}
	elsewhere := peerTarget{w: &TraceWorkload{Namespace: "team-b", NSLabels: nsLabels, PodLabels: podLabels, IPs: []string{"10.128.3.7"}}}
	external := peerTarget{ip: "203.0.113.9"}

	peers := []struct {
		name               string
		rule               rule
		summary            string
		vm, elsewhere, ext match
	}{
		{"netpol no peers", netpolPeer(), "", matchYes, matchYes, matchYes},
		{"netpol namespaceSelector", netpolPeer(map[string]any{"namespaceSelector": prod}),
			"ns env=prod", matchYes, matchYes, matchNo},
		{"netpol podSelector is the own namespace", netpolPeer(map[string]any{"podSelector": web}),
			"pods app=web", matchYes, matchNo, matchNo},
		{"netpol both selectors", netpolPeer(map[string]any{"namespaceSelector": prod, "podSelector": web}),
			"ns env=prod pods app=web", matchYes, matchYes, matchNo},
		{"netpol empty podSelector", netpolPeer(map[string]any{"namespaceSelector": prod, "podSelector": map[string]any{}}),
			"ns env=prod pods any", matchYes, matchYes, matchNo},
		{"netpol ipBlock", netpolPeer(map[string]any{"ipBlock": map[string]any{"cidr": "10.128.0.0/14"}}),
			"cidr 10.128.0.0/14", matchYes, matchYes, matchNo},
		{"netpol ipBlock except", netpolPeer(map[string]any{"ipBlock": map[string]any{"cidr": "10.128.0.0/14", "except": []any{"10.128.2.0/24"}}}),
			"cidr 10.128.0.0/14", matchNo, matchYes, matchNo},
		{"netpol two peers", netpolPeer(map[string]any{"podSelector": web}, map[string]any{"ipBlock": map[string]any{"cidr": "203.0.113.0/24"}}),
			"pods app=web; cidr 203.0.113.0/24", matchYes, matchNo, matchYes},
		{"anp namespaces", anpPeer(map[string]any{"namespaces": prod}), "ns env=prod", matchYes, matchYes, matchNo},
		{"anp all namespaces", anpPeer(map[string]any{"namespaces": map[string]any{}}), "ns any", matchYes, matchYes, matchNo},
		{"anp pods", anpPeer(map[string]any{"pods": map[string]any{"namespaceSelector": prod, "podSelector": web}}),
			"ns env=prod pods app=web", matchYes, matchYes, matchNo},
		{"anp pods empty podSelector", anpPeer(map[string]any{"pods": map[string]any{"namespaceSelector": prod, "podSelector": map[string]any{}}}),
			"ns env=prod", matchYes, matchYes, matchNo},
		{"anp networks", anpPeer(map[string]any{"networks": []any{"10.0.0.0/8", "203.0.113.0/24"}}),
			"cidr 10.0.0.0/8, 203.0.113.0/24", matchYes, matchYes, matchYes},
		{"anp nodes", anpPeer(map[string]any{"nodes": map[string]any{}}), "nodes any", matchNo, matchNo, matchCond},
		{"gateway cidrSelector", gatewayPeer(map[string]any{"cidrSelector": "203.0.113.0/24"}),
			"203.0.113.0/24", matchNo, matchNo, matchYes},
		{"gateway dnsName", gatewayPeer(map[string]any{"dnsName": "mirror.example.com"}),
			"mirror.example.com", matchCond, matchCond, matchCond},
		{"gateway nodeSelector", gatewayPeer(map[string]any{"nodeSelector": map[string]any{}}),
			"cluster nodes", matchNo, matchNo, matchCond},
		{"gateway unknown destination", gatewayPeer(map[string]any{}),
			"unresolved destination", matchCond, matchCond, matchCond},
	}
	for _, c := range peers {
		if got := c.rule.view("Ingress").Peer; got != c.summary {
			t.Errorf("%s: summary = %q, want %q", c.name, got, c.summary)
		}
		for _, tt := range []struct {
			target peerTarget
			want   match
			label  string
		}{{vm, c.vm, "vm"}, {elsewhere, c.elsewhere, "other-namespace vm"}, {external, c.ext, "external"}} {
			if got := c.rule.match(tt.target, "TCP", 443).m; got != tt.want {
				t.Errorf("%s vs %s: match = %v, want %v", c.name, tt.label, got, tt.want)
			}
		}
	}

	netpolPorts := func(ports ...map[string]any) rule {
		list := make([]any, 0, len(ports))
		for _, p := range ports {
			list = append(list, p)
		}
		p := decodeNetpol(&unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"name": "r", "namespace": "team-a"},
			"spec":     map[string]any{"podSelector": map[string]any{}, "ingress": []any{map[string]any{"ports": list}}},
		}})
		return p.ingress[0]
	}
	anpPorts := func(ports ...map[string]any) rule {
		list := make([]any, 0, len(ports))
		for _, p := range ports {
			list = append(list, p)
		}
		p := decodeANP(&unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"name": "r"},
			"spec": map[string]any{
				"subject": map[string]any{"namespaces": map[string]any{}},
				"egress":  []any{map[string]any{"action": "Allow", "to": []any{map[string]any{"namespaces": map[string]any{}}}, "ports": list}},
			},
		}})
		return p.egress[0]
	}
	ports := []struct {
		name          string
		rule          rule
		summary       string
		tcp443, tcp80 match
		anyPort       match
	}{
		{"no ports", netpolPorts(), "", matchYes, matchYes, matchYes},
		{"netpol port", netpolPorts(map[string]any{"protocol": "TCP", "port": int64(443)}), "TCP/443", matchYes, matchNo, matchCond},
		{"netpol range", netpolPorts(map[string]any{"protocol": "TCP", "port": int64(80), "endPort": int64(90)}), "TCP/80-90", matchNo, matchYes, matchCond},
		{"netpol protocol only", netpolPorts(map[string]any{"protocol": "TCP"}), "TCP", matchYes, matchYes, matchYes},
		{"netpol other protocol", netpolPorts(map[string]any{"protocol": "UDP", "port": int64(443)}), "UDP/443", matchNo, matchNo, matchNo},
		{"netpol named port", netpolPorts(map[string]any{"protocol": "TCP", "port": "web"}), "TCP/web", matchCond, matchCond, matchCond},
		{"anp portNumber", anpPorts(map[string]any{"portNumber": map[string]any{"protocol": "TCP", "port": int64(443)}}), "TCP/443", matchYes, matchNo, matchCond},
		{"anp portRange", anpPorts(map[string]any{"portRange": map[string]any{"protocol": "TCP", "start": int64(1), "end": int64(100)}}), "TCP/1-100", matchNo, matchYes, matchCond},
		{"anp namedPort", anpPorts(map[string]any{"namedPort": "web"}), "web", matchCond, matchCond, matchCond},
		{"two entries", netpolPorts(map[string]any{"protocol": "TCP", "port": int64(443)}, map[string]any{"protocol": "TCP", "port": int64(80)}), "TCP/443, TCP/80", matchYes, matchYes, matchCond},
	}
	for _, c := range ports {
		if got := c.rule.view("Ingress").Ports; got != c.summary {
			t.Errorf("%s: summary = %q, want %q", c.name, got, c.summary)
		}
		for _, tt := range []struct {
			port  int
			want  match
			label string
		}{{443, c.tcp443, "TCP/443"}, {80, c.tcp80, "TCP/80"}, {0, c.anyPort, "any port"}} {
			if got := c.rule.match(vm, "TCP", tt.port).m; got != tt.want {
				t.Errorf("%s vs %s: match = %v, want %v", c.name, tt.label, got, tt.want)
			}
		}
	}
}
