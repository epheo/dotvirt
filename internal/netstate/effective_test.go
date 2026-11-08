package netstate

import (
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/epheo/dotvirt/internal/model"
)

func nameInSelector(vals ...any) map[string]any {
	return map[string]any{"matchExpressions": []any{map[string]any{
		"key": "kubernetes.io/metadata.name", "operator": "In", "values": vals,
	}}}
}

// The east-west chain must come out in evaluation order - admin ANPs by
// precedence, then the selecting NetworkPolicies, then baseline - with
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
// exist - a netpol with egress rules and no policyTypes default-denies both.
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

// The egress planes: outbound rules bind by namespace; EgressIP and
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

// Egress planes bind in name order, not the store's hash order.
func TestEffectiveEgressPlanesOrdered(t *testing.T) {
	s := New(nil, nil)
	names := []string{"pool-d", "pool-b", "pool-a", "pool-c"}
	for _, name := range names {
		add(t, s.egressip, map[string]any{
			"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP",
			"metadata": map[string]any{"name": name},
			"spec":     map[string]any{"egressIPs": []any{"192.0.2.10"}, "namespaceSelector": map[string]any{}},
		})
	}
	for range 50 {
		eff := s.Effective("team-a", nil, nil, true)
		var got []string
		for _, b := range eff.SNAT {
			got = append(got, b.Policy.Name)
		}
		if !slices.Equal(got, []string{"pool-a", "pool-b", "pool-c", "pool-d"}) {
			t.Fatalf("SNAT order = %v", got)
		}
	}
}

// The Effective view flags what the trace treats as uncertain: two admin
// policies sharing a priority, and one past the range OVN-K enforces.
func TestEffectiveANPPriorityNotes(t *testing.T) {
	s := New(nil, nil)
	for name, prio := range map[string]int64{"a-shared": 5, "b-shared": 5, "solo": 7, "too-low": 500} {
		add(t, s.anp, map[string]any{
			"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
			"metadata": map[string]any{"name": name},
			"spec":     map[string]any{"priority": prio, "subject": map[string]any{"namespaces": map[string]any{}}},
		})
	}
	notes := map[string]string{}
	for _, b := range s.Effective("team-a", nil, nil, true).EastWest {
		notes[b.Policy.Name] = b.Note
	}
	for _, name := range []string{"a-shared", "b-shared"} {
		if !strings.Contains(notes[name], "same priority") {
			t.Errorf("%s note = %q, want the shared-priority flag", name, notes[name])
		}
	}
	if notes["solo"] != "" {
		t.Errorf("solo note = %q, want none", notes["solo"])
	}
	if !strings.Contains(notes["too-low"], "does not enforce priority 500") {
		t.Errorf("too-low note = %q, want the unenforced flag", notes["too-low"])
	}
}

func expr(key string, op metav1.LabelSelectorOperator, values ...string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: key, Operator: op, Values: values}}}
}

// A selector parsed at decode keeps its raw form for rendering and answers as
// the library does: absent or empty selects everything, and one the library
// rejects stays conditional, never a miss.
func TestSelectorMatch(t *testing.T) {
	web := map[string]string{"app": "web", "tier": "front"}
	both := &metav1.LabelSelector{
		MatchLabels:      map[string]string{"app": "web"},
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "tier", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"back"}}},
	}
	cases := []struct {
		name string
		sel  *metav1.LabelSelector
		lbls map[string]string
		want match
	}{
		{"nil", nil, web, matchYes},
		{"nil against no labels", nil, nil, matchYes},
		{"empty", &metav1.LabelSelector{}, nil, matchYes},
		{"empty, non-nil fields", &metav1.LabelSelector{MatchLabels: map[string]string{}, MatchExpressions: []metav1.LabelSelectorRequirement{}}, web, matchYes},
		{"labels hit", &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}, web, matchYes},
		{"labels miss", &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}, web, matchNo},
		{"labels against no labels", &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}, nil, matchNo},
		{"in", expr("tier", metav1.LabelSelectorOpIn, "front", "back"), web, matchYes},
		{"in miss", expr("tier", metav1.LabelSelectorOpIn, "back"), web, matchNo},
		{"notin", expr("tier", metav1.LabelSelectorOpNotIn, "front"), web, matchNo},
		{"notin absent key", expr("zone", metav1.LabelSelectorOpNotIn, "a"), web, matchYes},
		{"exists", expr("app", metav1.LabelSelectorOpExists), web, matchYes},
		{"does not exist", expr("app", metav1.LabelSelectorOpDoesNotExist), web, matchNo},
		{"labels and expressions", both, web, matchYes},
		{"labels and expressions miss", both, map[string]string{"app": "web", "tier": "back"}, matchNo},
		{"unknown operator", expr("app", "Bogus"), web, matchCond},
		{"in without values", expr("app", metav1.LabelSelectorOpIn), web, matchCond},
		{"invalid key", &metav1.LabelSelector{MatchLabels: map[string]string{"bad key": "x"}}, web, matchCond},
		{"rejected against no labels", expr("app", "Bogus"), nil, matchCond},
	}
	for _, c := range cases {
		sel := parseSelector(c.sel)
		if sel.raw != c.sel {
			t.Errorf("%s: raw selector not kept", c.name)
		}
		if got := sel.match(c.lbls); got != c.want {
			t.Errorf("%s: match = %d, want %d", c.name, got, c.want)
		}
		if got := libraryMatch(c.sel, c.lbls); got != c.want {
			t.Errorf("%s: library answers %d, want %d", c.name, got, c.want)
		}
	}
	unparsed := selector{raw: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}}
	if got := unparsed.match(web); got != matchCond {
		t.Errorf("a selector never parsed: match = %d, want conditional", got)
	}
}

// A pod selector that selects every pod is definite even for a
// namespace-level query; any other is conditional there.
func TestPodMatch(t *testing.T) {
	web := map[string]string{"app": "web"}
	cases := []struct {
		name      string
		sel       *metav1.LabelSelector
		podScoped bool
		want      match
	}{
		{"nil, namespace-level", nil, false, matchYes},
		{"empty, namespace-level", &metav1.LabelSelector{}, false, matchYes},
		{"labels, namespace-level", &metav1.LabelSelector{MatchLabels: web}, false, matchCond},
		{"labels hit", &metav1.LabelSelector{MatchLabels: web}, true, matchYes},
		{"labels miss", &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}, true, matchNo},
		{"rejected, namespace-level", expr("app", "Bogus"), false, matchCond},
		{"rejected", expr("app", "Bogus"), true, matchCond},
	}
	for _, c := range cases {
		if got := podMatch(parseSelector(c.sel), web, c.podScoped); got != c.want {
			t.Errorf("%s: podMatch = %d, want %d", c.name, got, c.want)
		}
	}
}

// libraryMatch is the library's answer for a raw selector, parsed at the call.
func libraryMatch(raw *metav1.LabelSelector, lbls map[string]string) match {
	if emptySelector(raw) {
		return matchYes
	}
	sl, err := metav1.LabelSelectorAsSelector(raw)
	if err != nil {
		return matchCond
	}
	if sl.Matches(labels.Set(lbls)) {
		return matchYes
	}
	return matchNo
}

// Every selector a decoded policy matches with, subject and peer alike,
// answers as the library does for its raw selector, and a policy whose
// subject the library rejects still binds, conditionally.
func TestDecodedSelectorsMatchLibrary(t *testing.T) {
	s := New(nil, nil)
	bogus := map[string]any{"matchExpressions": []any{map[string]any{"key": "env", "operator": "Bogus"}}}
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "web", "namespace": "team-a"},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			"ingress": []any{map[string]any{"from": []any{
				map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"env": "prod"}}, "podSelector": map[string]any{"matchLabels": map[string]any{"app": "db"}}},
				map[string]any{"podSelector": map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "NotIn", "values": []any{"web"}}}}},
				map[string]any{"namespaceSelector": bogus},
			}}},
		},
	})
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "ns-subject"},
		"spec": map[string]any{
			"priority": int64(10),
			"subject": map[string]any{"namespaces": map[string]any{"matchExpressions": []any{
				map[string]any{"key": "env", "operator": "In", "values": []any{"prod", "stage"}},
			}}},
			"ingress": []any{map[string]any{"action": "Allow", "from": []any{
				map[string]any{"namespaces": map[string]any{"matchLabels": map[string]any{"env": "dev"}}},
				map[string]any{"pods": map[string]any{
					"namespaceSelector": map[string]any{"matchExpressions": []any{map[string]any{"key": "env", "operator": "Exists"}}},
					"podSelector":       map[string]any{"matchLabels": map[string]any{"app": "monitor"}},
				}},
			}}},
		},
	})
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "pods-subject"},
		"spec": map[string]any{
			"priority": int64(20),
			"subject": map[string]any{"pods": map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"env": "prod"}},
				"podSelector":       map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "DoesNotExist"}}},
			}},
		},
	})
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "unreadable"},
		"spec":     map[string]any{"priority": int64(30), "subject": map[string]any{"namespaces": bogus}},
	})
	add(t, s.banp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "BaselineAdminNetworkPolicy",
		"metadata": map[string]any{"name": "default"},
		"spec": map[string]any{
			"subject": map[string]any{"namespaces": map[string]any{}},
			"ingress": []any{map[string]any{"action": "Deny", "from": []any{map[string]any{"namespaces": map[string]any{}}}}},
		},
	})
	add(t, s.egressip, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP",
		"metadata": map[string]any{"name": "pool"},
		"spec": map[string]any{
			"egressIPs":         []any{"192.0.2.10"},
			"namespaceSelector": map[string]any{"matchLabels": map[string]any{"env": "prod"}},
			"podSelector":       map[string]any{"matchLabels": map[string]any{"app": "web"}},
		},
	})
	add(t, s.extroute, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "AdminPolicyBasedExternalRoute",
		"metadata": map[string]any{"name": "route"},
		"spec": map[string]any{
			"from":     map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"env": "dev"}}},
			"nextHops": map[string]any{"static": []any{map[string]any{"ip": "198.51.100.1"}}},
		},
	})

	var sels []selector
	for _, p := range slices.Concat(s.sortedANPs(), s.baselines(), s.netpols.plane().all, s.egressIPs.plane().all, s.extRoutes.plane().all) {
		sels = append(sels, p.subject.ns, p.subject.pod)
		for _, r := range slices.Concat(p.ingress, p.egress) {
			for _, pe := range r.peers {
				sels = append(sels, pe.ns, pe.pod)
			}
		}
	}
	sets := []map[string]string{
		nil,
		{"env": "prod", "app": "web"},
		{"env": "prod", "app": "db"},
		{"env": "dev", "app": "monitor"},
		{"env": "stage"},
	}
	seen := map[match]bool{}
	for _, sel := range sels {
		for _, lbls := range sets {
			got, want := sel.match(lbls), libraryMatch(sel.raw, lbls)
			if got != want {
				t.Errorf("selector %s against %v: match = %d, library answers %d", metav1.FormatLabelSelector(sel.raw), lbls, got, want)
			}
			seen[got] = true
		}
	}
	if !seen[matchYes] || !seen[matchNo] || !seen[matchCond] {
		t.Errorf("verdicts covered: %v, want yes, no and conditional", seen)
	}

	eff := s.Effective("team-b", map[string]string{"env": "prod"}, map[string]string{"app": "web"}, true)
	got := map[string]bool{}
	for _, b := range eff.EastWest {
		got[b.Policy.Name] = b.Conditional
	}
	if c, ok := got["unreadable"]; !ok || !c {
		t.Errorf("a rejected subject must bind conditionally: %v", got)
	}
	if c, ok := got["ns-subject"]; !ok || c {
		t.Errorf("a matching subject must bind definitely: %v", got)
	}
	if _, ok := got["pods-subject"]; ok {
		t.Errorf("a pod subject that misses must not bind: %v", got)
	}
}
