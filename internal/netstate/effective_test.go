package netstate

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

func nameInSelector(vals ...any) map[string]any {
	return map[string]any{"matchExpressions": []any{map[string]any{
		"key": "kubernetes.io/metadata.name", "operator": "In", "values": vals,
	}}}
}

// The east-west chain must come out in evaluation order — admin ANPs by
// precedence, then the selecting NetworkPolicies, then baseline — with
// non-binding policies absent and a definite netpol selection flipping the
// default-deny flags per declared direction.
func TestEffectiveEastWestOrder(t *testing.T) {
	s := New(nil, nil)

	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "later"},
		"spec": map[string]any{
			"priority": int64(20),
			"subject":  map[string]any{"namespaces": map[string]any{"matchLabels": map[string]any{"env": "prod"}}},
		},
	})
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "first"},
		"spec": map[string]any{
			"priority": int64(5),
			"subject":  map[string]any{"namespaces": nameInSelector("team-a")},
		},
	})
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "elsewhere"},
		"spec": map[string]any{
			"priority": int64(1),
			"subject":  map[string]any{"namespaces": map[string]any{"matchLabels": map[string]any{"env": "dev"}}},
		},
	})
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "web-ingress", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			"policyTypes": []any{"Ingress"},
		},
	})
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "db-only", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "db"}},
		},
	})
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "other-ns", "namespace": "team-b"},
		"spec":     map[string]any{"podSelector": map[string]any{}},
	})
	add(t, s.banp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "BaselineAdminNetworkPolicy",
		"metadata": map[string]any{"name": "default"},
		"spec":     map[string]any{"subject": map[string]any{"namespaces": map[string]any{}}},
	})

	nsLabels := map[string]string{"kubernetes.io/metadata.name": "team-a", "env": "prod"}
	eff := s.Effective("team-a", nsLabels, map[string]string{"app": "web"}, true)

	var names []string
	for _, b := range eff.EastWest {
		names = append(names, string(b.Policy.Kind)+":"+b.Policy.Name)
	}
	want := []string{"admin:first", "admin:later", "dfw:web-ingress", "baseline:default"}
	if len(names) != len(want) {
		t.Fatalf("chain = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("chain = %v, want %v", names, want)
		}
	}
	if !eff.DefaultDenyIngress || eff.DefaultDenyEgress {
		t.Errorf("selected by an Ingress netpol: want default-deny ingress only, got ingress=%v egress=%v",
			eff.DefaultDenyIngress, eff.DefaultDenyEgress)
	}
	if eff.EastWest[3].Note == "" {
		t.Errorf("baseline binding must carry its only-if-undecided note")
	}
	for _, b := range eff.EastWest {
		if b.Conditional {
			t.Errorf("pod-scoped query with definite selectors: %s must not be conditional", b.Policy.Name)
		}
	}
}

// policyTypes defaulting: absent means Ingress, plus Egress when egress rules
// exist — a netpol with egress rules and no policyTypes default-denies both.
func TestEffectiveNetpolTypeDefaulting(t *testing.T) {
	s := New(nil, nil)
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "eg", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"egress":      []any{map[string]any{"to": []any{}}},
		},
	})
	eff := s.Effective("team-a", nil, nil, true)
	if !eff.DefaultDenyIngress || !eff.DefaultDenyEgress {
		t.Errorf("egress rules without policyTypes: want both directions denied, got ingress=%v egress=%v",
			eff.DefaultDenyIngress, eff.DefaultDenyEgress)
	}
}

// A namespace-level query cannot resolve pod selectors: a selecting netpol
// with a non-empty podSelector binds conditionally (and must not set the
// default-deny flags), while an empty podSelector stays definite.
func TestEffectiveNamespaceScopeConditional(t *testing.T) {
	s := New(nil, nil)
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "web-only", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			"policyTypes": []any{"Ingress"},
		},
	})
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "deny-all", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"policyTypes": []any{"Egress"},
		},
	})

	eff := s.Effective("team-a", nil, nil, false)
	if len(eff.EastWest) != 2 {
		t.Fatalf("want both netpols bound, got %+v", eff.EastWest)
	}
	byName := map[string]model.PolicyBinding{}
	for _, b := range eff.EastWest {
		byName[b.Policy.Name] = b
	}
	if !byName["web-only"].Conditional {
		t.Errorf("pod-selecting netpol must be conditional at namespace scope")
	}
	if byName["deny-all"].Conditional {
		t.Errorf("empty podSelector selects the whole namespace: must be definite")
	}
	if eff.DefaultDenyIngress {
		t.Errorf("conditional selection must not claim default-deny ingress")
	}
	if !eff.DefaultDenyEgress {
		t.Errorf("definite whole-namespace Egress netpol must set default-deny egress")
	}
}

// Pod labels decide: the same ANP pods-subject binds one workload and not
// another in the same namespace.
func TestEffectiveANPPodSubject(t *testing.T) {
	s := New(nil, nil)
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "lockdown-web"},
		"spec": map[string]any{
			"priority": int64(3),
			"subject": map[string]any{"pods": map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"env": "prod"}},
				"podSelector":       map[string]any{"matchLabels": map[string]any{"app": "web"}},
			}},
		},
	})
	nsLabels := map[string]string{"env": "prod"}
	if eff := s.Effective("team-a", nsLabels, map[string]string{"app": "web"}, true); len(eff.EastWest) != 1 {
		t.Errorf("web pod in prod namespace: want the ANP bound, got %+v", eff.EastWest)
	}
	if eff := s.Effective("team-a", nsLabels, map[string]string{"app": "db"}, true); len(eff.EastWest) != 0 {
		t.Errorf("db pod: want no binding, got %+v", eff.EastWest)
	}
	if eff := s.Effective("team-b", map[string]string{"env": "dev"}, map[string]string{"app": "web"}, true); len(eff.EastWest) != 0 {
		t.Errorf("dev namespace: want no binding, got %+v", eff.EastWest)
	}
	// Namespace scope: the namespace side matches, the pod side can't resolve.
	if eff := s.Effective("team-a", nsLabels, nil, false); len(eff.EastWest) != 1 || !eff.EastWest[0].Conditional {
		t.Errorf("namespace scope: want one conditional binding, got %+v", eff.EastWest)
	}
}

// The egress planes: gateway firewall binds by namespace; EgressIP and
// external routes bind by namespace selector, with EgressIP's optional
// podSelector narrowing conditionally at namespace scope.
func TestEffectiveEgressPlanes(t *testing.T) {
	s := New(nil, nil)
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "team-a"},
		"spec": map[string]any{"egress": []any{
			map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}},
		}},
	})
	add(t, s.egressip, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP",
		"metadata": map[string]any{"name": "prod-egress"},
		"spec": map[string]any{
			"egressIPs":         []any{"192.0.2.10"},
			"namespaceSelector": nameInSelector("team-a"),
			"podSelector":       map[string]any{"matchLabels": map[string]any{"tier": "backend"}},
		},
	})
	add(t, s.extroute, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "AdminPolicyBasedExternalRoute",
		"metadata": map[string]any{"name": "dmz"},
		"spec": map[string]any{
			"from":     map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"tier": "dmz"}}},
			"nextHops": map[string]any{"static": []any{map[string]any{"ip": "10.0.0.1"}}},
		},
	})

	nsLabels := map[string]string{"kubernetes.io/metadata.name": "team-a"}

	eff := s.Effective("team-a", nsLabels, map[string]string{"tier": "backend"}, true)
	if len(eff.Gateway) != 1 || len(eff.SNAT) != 1 || len(eff.Routes) != 0 {
		t.Fatalf("backend pod: gateway=%d snat=%d routes=%d", len(eff.Gateway), len(eff.SNAT), len(eff.Routes))
	}
	if eff.SNAT[0].Conditional {
		t.Errorf("pod labels match the EgressIP podSelector: must be definite")
	}

	if eff := s.Effective("team-a", nsLabels, map[string]string{"tier": "frontend"}, true); len(eff.SNAT) != 0 {
		t.Errorf("frontend pod: EgressIP podSelector must exclude it, got %+v", eff.SNAT)
	}

	if eff := s.Effective("team-a", nsLabels, nil, false); len(eff.SNAT) != 1 || !eff.SNAT[0].Conditional {
		t.Errorf("namespace scope: EgressIP with podSelector must bind conditionally, got %+v", eff.SNAT)
	}

	if eff := s.Effective("team-b", map[string]string{"kubernetes.io/metadata.name": "team-b"}, nil, false); len(eff.Gateway)+len(eff.SNAT)+len(eff.Routes) != 0 {
		t.Errorf("team-b: nothing binds, got %+v", eff)
	}
}
