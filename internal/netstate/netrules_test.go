package netstate

import (
	"strings"
	"testing"
)

func TestNetworkRules(t *testing.T) {
	s := New(nil, nil)
	nsLabels := func(ns string) map[string]string {
		l := map[string]string{"kubernetes.io/metadata.name": ns}
		if ns == "web-prod" {
			l["env"] = "prod"
		}
		return l
	}
	nameIn := func(ns ...any) map[string]any {
		return map[string]any{"matchExpressions": []any{map[string]any{
			"key": "kubernetes.io/metadata.name", "operator": "In", "values": ns,
		}}}
	}
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "deny-lateral"},
		"spec": map[string]any{
			"priority": int64(10),
			"subject":  map[string]any{"namespaces": map[string]any{}},
			"ingress":  []any{map[string]any{"action": "Deny", "from": []any{map[string]any{"namespaces": map[string]any{}}}}},
		},
	})
	// Binds web-prod and names it as a peer: listed once, as binding.
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "allow-https", "namespace": "web-prod"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"ingress": []any{map[string]any{
				"from":  []any{map[string]any{"podSelector": map[string]any{}}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": int64(443)}},
			}},
		},
	})
	// Elsewhere, naming web-prod's app=web VMs: a conditional peer rule.
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
	// Elsewhere, an egress to prod namespaces: definite (no pod selector).
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "scrape-prod", "namespace": "monitoring"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"egress": []any{map[string]any{
				"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"env": "prod"}}}},
			}},
		},
	})
	// Same-namespace peer elsewhere: never web-prod's.
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "internal", "namespace": "db-prod"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"ingress":     []any{map[string]any{"from": []any{map[string]any{"podSelector": map[string]any{}}}}},
		},
	})
	add(t, s.egressip, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP",
		"metadata": map[string]any{"name": "web-egress"},
		"spec":     map[string]any{"egressIPs": []any{"192.0.2.10"}, "namespaceSelector": nameIn("web-prod")},
	})

	got := s.NetworkRules([]string{"web-prod"}, nsLabels, nil, "")
	var applies []string
	for _, b := range got.Applies {
		applies = append(applies, b.Policy.Name)
	}
	if !eqStrings(applies, []string{"deny-lateral", "allow-https", "web-egress"}) {
		t.Errorf("applies = %v", applies)
	}
	type reach struct {
		name, text string
		cond       bool
	}
	var reaches []reach
	for _, r := range got.Reaches {
		reaches = append(reaches, reach{r.Policy.Name, r.Text, r.Conditional})
	}
	want := []reach{
		{"allow-web-to-db", "VMs labeled app=web in web-prod can reach VMs labeled app=db in db-prod on TCP 5432.", true},
		{"scrape-prod", "Every VM in monitoring can reach VMs in namespaces labeled env=prod.", false},
	}
	if len(reaches) != len(want) {
		t.Fatalf("reaches = %+v, want %+v", reaches, want)
	}
	for i := range want {
		if reaches[i] != want[i] {
			t.Errorf("reach[%d] = %+v, want %+v", i, reaches[i], want[i])
		}
	}

	// A shared network across two namespaces lists a policy once.
	both := s.NetworkRules([]string{"web-prod", "db-prod"}, nsLabels, nil, "")
	n := 0
	for _, b := range both.Applies {
		if b.Policy.Name == "deny-lateral" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("deny-lateral listed %d times across two namespaces", n)
	}
}

// A rule elsewhere either names the network's namespace (by name or by
// project) or merely matches it; the platform's "any namespace" rules are the
// second kind, ranked after the first.
func TestNetworkRulesWildcards(t *testing.T) {
	s := New(nil, nil)
	nsLabels := func(ns string) map[string]string {
		return map[string]string{"kubernetes.io/metadata.name": ns, "dotvirt.io/project": "team-web", "env": "prod"}
	}
	peerRule := func(ns, name string, from map[string]any) {
		add(t, s.netpol, map[string]any{
			"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
			"metadata": map[string]any{"name": name, "namespace": ns},
			"spec": map[string]any{
				"podSelector": map[string]any{},
				"ingress":     []any{map[string]any{"from": []any{from}}},
			},
		})
	}
	peerRule("openshift-gitops", "any-ns", map[string]any{"namespaceSelector": map[string]any{}})
	peerRule("monitoring", "by-label", map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"env": "prod"}}})
	peerRule("db-prod", "by-name", map[string]any{"namespaceSelector": map[string]any{"matchExpressions": []any{map[string]any{
		"key": "kubernetes.io/metadata.name", "operator": "In", "values": []any{"web-prod"},
	}}}})
	peerRule("cache", "by-project", map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"dotvirt.io/project": "team-web"}}})
	// Two alternatives: the one naming web-prod decides, so it is no wildcard.
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "both", "namespace": "search"},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"ingress": []any{map[string]any{"from": []any{
				map[string]any{"namespaceSelector": map[string]any{}},
				map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "web-prod"}}},
			}}},
		},
	})

	got := s.NetworkRules([]string{"web-prod"}, nsLabels, nil, "dotvirt.io/project")
	var named, wild []string
	for _, r := range got.Reaches {
		if r.Wildcard {
			wild = append(wild, r.Policy.Name)
		} else {
			named = append(named, r.Policy.Name)
		}
	}
	if !eqStrings(named, []string{"by-project", "by-name", "both"}) {
		t.Errorf("named = %v", named)
	}
	if !eqStrings(wild, []string{"by-label", "any-ns"}) {
		t.Errorf("wildcards = %v", wild)
	}
	if got.Reaches[len(got.Reaches)-1].Wildcard != true || got.Reaches[0].Wildcard {
		t.Error("named rules must rank before wildcards")
	}
	for _, r := range got.Reaches {
		if r.Policy.Name == "both" && !strings.Contains(r.Text, "web-prod") {
			t.Errorf("the naming alternative should decide the sentence: %q", r.Text)
		}
	}
	// Without a project label configured, a project selector only matches.
	noLabel := s.NetworkRules([]string{"web-prod"}, nsLabels, nil, "")
	for _, r := range noLabel.Reaches {
		if r.Policy.Name == "by-project" && !r.Wildcard {
			t.Error("a project selector names nothing without the project label")
		}
	}
}
