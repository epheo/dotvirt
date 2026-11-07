package netstate

import (
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

func ruleTexts(p model.Policy) []string {
	out := make([]string, 0, len(p.Rules))
	for _, r := range p.Rules {
		out = append(out, r.Text)
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPolicySentences(t *testing.T) {
	s := New(nil, nil)
	nameIn := func(ns ...any) map[string]any {
		return map[string]any{"matchExpressions": []any{map[string]any{
			"key": "kubernetes.io/metadata.name", "operator": "In", "values": ns,
		}}}
	}
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "allow-web-to-db", "namespace": "db-prod"},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "db"}},
			"ingress": []any{map[string]any{
				"from": []any{map[string]any{
					"namespaceSelector": nameIn("web-prod"),
					"podSelector":       map[string]any{"matchLabels": map[string]any{"app": "web"}},
				}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": int64(5432)}},
			}},
		},
	})
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "allow-https", "namespace": "web-prod"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"ingress": []any{map[string]any{
				"from":  []any{map[string]any{"podSelector": map[string]any{}}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": int64(443)}, map[string]any{"protocol": "UDP", "port": int64(53), "endPort": int64(54)}},
			}},
			"egress": []any{map[string]any{
				"to": []any{map[string]any{"ipBlock": map[string]any{"cidr": "0.0.0.0/0", "except": []any{"10.0.0.0/8"}}}},
			}},
		},
	})
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "deny-all", "namespace": "web-prod"},
		"spec":     map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress", "Egress"}},
	})
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "deny-lateral"},
		"spec": map[string]any{
			"priority": int64(10),
			"subject":  map[string]any{"namespaces": map[string]any{}},
			"ingress": []any{
				map[string]any{"action": "Pass", "from": []any{map[string]any{"namespaces": map[string]any{"matchLabels": map[string]any{"tier": "infra"}}}}},
				map[string]any{"action": "Deny", "from": []any{map[string]any{"namespaces": map[string]any{}}}},
			},
			"egress": []any{map[string]any{
				"action": "Deny",
				"to":     []any{map[string]any{"nodes": map[string]any{}}},
				"ports":  []any{map[string]any{"namedPort": "metrics"}},
			}},
		},
	})
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "web-prod"},
		"spec": map[string]any{"egress": []any{
			map[string]any{"type": "Allow", "to": map[string]any{"dnsName": "mirror.example.com"}, "ports": []any{map[string]any{"protocol": "TCP", "port": int64(443)}}},
			map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}},
		}},
	})
	add(t, s.egressip, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP",
		"metadata": map[string]any{"name": "web-egress"},
		"spec": map[string]any{
			"egressIPs":         []any{"192.0.2.10", "192.0.2.11"},
			"namespaceSelector": nameIn("web-prod"),
			"podSelector":       map[string]any{"matchLabels": map[string]any{"app": "web"}},
		},
	})

	by := map[string]model.Policy{}
	for _, p := range s.Policies() {
		by[p.Name] = p
	}
	cases := []struct {
		name, applies, isolation string
		texts                    []string
	}{
		{"allow-web-to-db", "VMs labeled app=db in db-prod", "Blocks other incoming traffic.",
			[]string{"Accepts TCP 5432 from VMs labeled app=web in web-prod."}},
		{"allow-https", "every VM in web-prod", "Blocks other incoming traffic. Blocks other outgoing traffic.",
			[]string{"Accepts TCP 443 and UDP 53-54 from VMs in web-prod.", "Can reach any address except 10.0.0.0/8."}},
		{"deny-all", "every VM in web-prod", "Blocks all incoming traffic. Blocks all outgoing traffic.", []string{}},
		{"deny-lateral", "every namespace", "", []string{
			"Leaves all traffic from VMs in namespaces labeled tier=infra to project rules.",
			"Blocks all traffic from VMs in any namespace.",
			"Cannot reach the cluster's hosts on port metrics.",
		}},
		{"default", "every VM in web-prod", "", []string{"Can reach mirror.example.com on TCP 443.", "Cannot reach anything outside."}},
		{"web-egress", "VMs labeled app=web in web-prod", "", []string{"Leaves with source IP 192.0.2.10 or 192.0.2.11."}},
	}
	for _, c := range cases {
		p, ok := by[c.name]
		if !ok {
			t.Fatalf("policy %s missing", c.name)
		}
		if p.Applies != c.applies {
			t.Errorf("%s applies = %q, want %q", c.name, p.Applies, c.applies)
		}
		if p.Isolation != c.isolation {
			t.Errorf("%s isolation = %q, want %q", c.name, p.Isolation, c.isolation)
		}
		if got := ruleTexts(p); !eqStrings(got, c.texts) {
			t.Errorf("%s texts = %q, want %q", c.name, got, c.texts)
		}
	}
}

// A tenant never reads another tenant's namespace or project in a sentence:
// subjects, rule sentences and peer-side sentences all go through the
// caller's redaction.
func TestSentencesRedactOtherTenants(t *testing.T) {
	s := New(nil, nil)
	nameIn := func(ns ...any) map[string]any {
		return map[string]any{"matchExpressions": []any{map[string]any{
			"key": "kubernetes.io/metadata.name", "operator": "In", "values": ns,
		}}}
	}
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "split"},
		"spec": map[string]any{
			"priority": int64(10),
			"subject":  map[string]any{"namespaces": nameIn("tenant-a", "tenant-b")},
			"ingress": []any{map[string]any{"action": "Deny", "from": []any{map[string]any{
				"namespaces": map[string]any{"matchLabels": map[string]any{"dotvirt.io/project": "team-b"}},
			}}}},
			"egress": []any{map[string]any{"action": "Allow", "to": []any{map[string]any{
				"pods": map[string]any{"namespaceSelector": nameIn("tenant-b"), "podSelector": map[string]any{"matchLabels": map[string]any{"app": "db"}}},
			}}}},
		},
	})
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "into-b"},
		"spec": map[string]any{
			"priority": int64(20),
			"subject":  map[string]any{"namespaces": nameIn("tenant-b")},
			"ingress":  []any{map[string]any{"action": "Allow", "from": []any{map[string]any{"namespaces": nameIn("tenant-a", "tenant-c")}}}},
		},
	})
	add(t, s.egressip, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP",
		"metadata": map[string]any{"name": "pool"},
		"spec":     map[string]any{"egressIPs": []any{"192.0.2.10"}, "namespaceSelector": nameIn("tenant-a", "tenant-b")},
	})
	red := &Redaction{
		ProjectLabel: "dotvirt.io/project",
		Namespace:    func(ns string) bool { return ns == "tenant-a" },
		Project:      func(p string) bool { return p == "team-a" },
	}
	leaks := func(where string, texts ...string) {
		t.Helper()
		for _, x := range texts {
			for _, other := range []string{"tenant-b", "tenant-c", "team-b"} {
				if strings.Contains(x, other) {
					t.Errorf("%s leaks %q: %q", where, other, x)
				}
			}
		}
	}
	for _, p := range s.Policies() {
		s.Redact(&p, nil, red)
		texts := []string{p.Target, p.Applies}
		for _, r := range p.Rules {
			texts = append(texts, r.Text, r.Peer)
		}
		leaks(p.Name, texts...)
	}
	nsLabels := func(ns string) map[string]string {
		return map[string]string{"kubernetes.io/metadata.name": ns, "dotvirt.io/project": "team-a"}
	}
	got := s.NetworkRules([]string{"tenant-a"}, nsLabels, red)
	if len(got.Reaches) != 1 {
		t.Fatalf("reaches = %+v, want into-b naming tenant-a", got.Reaches)
	}
	leaks("into-b peer sentence", got.Reaches[0].Text)
	if !strings.Contains(got.Reaches[0].Text, "tenant-a") || !strings.Contains(got.Reaches[0].Text, "other namespaces") {
		t.Errorf("peer sentence = %q, want tenant-a named and the rest hidden", got.Reaches[0].Text)
	}
}
