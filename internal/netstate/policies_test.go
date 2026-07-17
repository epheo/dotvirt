package netstate

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/model"
)

func add(t *testing.T, idx interface{ Add(any) error }, obj map[string]any) {
	t.Helper()
	if err := idx.Add(&unstructured.Unstructured{Object: obj}); err != nil {
		t.Fatal(err)
	}
}

func TestPolicies(t *testing.T) {
	s := New(nil, nil)

	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "web-ingress", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			"ingress": []any{map[string]any{
				"from":  []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "lb"}}}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": int64(443)}},
			}},
		},
	})
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "block-dev"},
		"spec": map[string]any{
			"priority": int64(10),
			"subject":  map[string]any{"namespaces": map[string]any{"matchLabels": map[string]any{"env": "prod"}}},
			"egress": []any{map[string]any{
				"action": "Deny",
				"to":     []any{map[string]any{"namespaces": map[string]any{"matchLabels": map[string]any{"env": "dev"}}}},
				"ports":  []any{map[string]any{"portNumber": map[string]any{"protocol": "TCP", "port": int64(5432)}}},
			}},
		},
	})
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "team-a"},
		"spec": map[string]any{"egress": []any{
			map[string]any{"type": "Allow", "to": map[string]any{"dnsName": "mirror.example.com"}},
			map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}},
		}},
	})
	add(t, s.egressip, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP",
		"metadata": map[string]any{"name": "prod-egress"},
		"spec": map[string]any{
			"egressIPs": []any{"192.0.2.10", "192.0.2.11"},
			"namespaceSelector": map[string]any{"matchExpressions": []any{map[string]any{
				"key": "kubernetes.io/metadata.name", "operator": "In", "values": []any{"team-a", "team-b"},
			}}},
		},
	})
	add(t, s.extroute, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "AdminPolicyBasedExternalRoute",
		"metadata": map[string]any{"name": "dmz-route"},
		"spec": map[string]any{
			"from":     map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"tier": "dmz"}}},
			"nextHops": map[string]any{"static": []any{map[string]any{"ip": "10.0.0.1"}}},
		},
	})

	got := s.Policies()
	if len(got) != 5 {
		t.Fatalf("want 5 policies, got %d: %+v", len(got), got)
	}

	// Tier order: admin, dfw, gateway, egressip, route.
	wantKinds := []model.PolicyKind{model.PolicyAdmin, model.PolicyDFW, model.PolicyGateway, model.PolicyEgressIP, model.PolicyRoute}
	for i, k := range wantKinds {
		if got[i].Kind != k {
			t.Fatalf("policy %d kind = %s, want %s", i, got[i].Kind, k)
		}
	}

	anp := got[0]
	if anp.Priority != 10 || anp.Target != "env=prod" {
		t.Errorf("anp target/priority wrong: %+v", anp)
	}
	if anp.Namespaces != nil {
		t.Errorf("label-selector anp must not enumerate namespaces: %v", anp.Namespaces)
	}
	if len(anp.Rules) != 1 || anp.Rules[0].Action != "Deny" || anp.Rules[0].Peer != "ns env=dev" || anp.Rules[0].Ports != "TCP/5432" {
		t.Errorf("anp rule wrong: %+v", anp.Rules)
	}

	np := got[1]
	if np.Namespace != "team-a" || np.Target != "app=web" {
		t.Errorf("netpol identity wrong: %+v", np)
	}
	if len(np.Rules) != 1 || np.Rules[0].Direction != "Ingress" || np.Rules[0].Peer != "pods app=lb" || np.Rules[0].Ports != "TCP/443" {
		t.Errorf("netpol rule wrong: %+v", np.Rules)
	}

	fw := got[2]
	if len(fw.Rules) != 2 || fw.Rules[0].Peer != "mirror.example.com" || fw.Rules[1].Action != "Deny" || fw.Rules[1].Peer != "0.0.0.0/0" {
		t.Errorf("egress firewall rules wrong: %+v", fw.Rules)
	}

	eip := got[3]
	if eip.Target != "team-a, team-b" {
		t.Errorf("egressip name-In selector should collapse to the namespace list: %q", eip.Target)
	}
	if len(eip.Namespaces) != 2 || eip.Namespaces[0] != "team-a" || eip.Namespaces[1] != "team-b" {
		t.Errorf("egressip name-In selector should enumerate namespaces: %v", eip.Namespaces)
	}
	if len(eip.Rules) != 1 || eip.Rules[0].Action != "SNAT" || eip.Rules[0].Peer != "192.0.2.10, 192.0.2.11" {
		t.Errorf("egressip rule wrong: %+v", eip.Rules)
	}

	rt := got[4]
	if rt.Target != "tier=dmz" || len(rt.Rules) != 1 || rt.Rules[0].Peer != "via 10.0.0.1" {
		t.Errorf("external route wrong: %+v", rt)
	}
	if rt.Namespaces != nil {
		t.Errorf("label-selector route must not enumerate namespaces: %v", rt.Namespaces)
	}
}

// The tenant filter hides a cluster row only when its namespaces are provably
// enumerated; every ambiguous selector must come out nil so the row stays.
func TestSelectorNamespaces(t *testing.T) {
	nameIn := func(vals ...any) map[string]any {
		return map[string]any{"matchExpressions": []any{map[string]any{
			"key": "kubernetes.io/metadata.name", "operator": "In", "values": vals,
		}}}
	}
	cases := []struct {
		name string
		sel  map[string]any
		want []string
	}{
		{"empty selector", map[string]any{}, nil},
		{"name-In", nameIn("a", "b"), []string{"a", "b"}},
		{"name-In empty values", nameIn(), nil},
		{"name matchLabel", map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "a"}}, []string{"a"}},
		{"other matchLabel", map[string]any{"matchLabels": map[string]any{"env": "prod"}}, nil},
		{"name-NotIn", map[string]any{"matchExpressions": []any{map[string]any{
			"key": "kubernetes.io/metadata.name", "operator": "NotIn", "values": []any{"a"},
		}}}, nil},
		{"name-In plus label", func() map[string]any {
			s := nameIn("a")
			s["matchLabels"] = map[string]any{"env": "prod"}
			return s
		}(), nil},
	}
	for _, c := range cases {
		got := selectorNamespaces(c.sel)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

// An empty-podSelector NetworkPolicy with a declared direction and no rules is
// the default-deny idiom — it must come out as "all pods" with zero rule rows,
// never invent a rule.
func TestPoliciesDefaultDeny(t *testing.T) {
	s := New(nil, nil)
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "deny-all", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"policyTypes": []any{"Ingress"},
		},
	})
	got := s.Policies()
	if len(got) != 1 || got[0].Target != "all pods" || len(got[0].Rules) != 0 {
		t.Errorf("default-deny wrong: %+v", got)
	}
}
