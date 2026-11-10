package netstate

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
)

// universe is a cluster of team-a (the caller's), team-b and kube-system,
// whose labels the snapshot does not hold.
func universe() []NamespaceLabels {
	return []NamespaceLabels{
		{Name: "team-a", Labels: map[string]string{"dotvirt.io/project": "team-a", "env": "prod"}},
		{Name: "team-b", Labels: map[string]string{"dotvirt.io/project": "team-b", "env": "prod"}},
		{Name: "kube-system"},
	}
}

// Redact hides, in a cluster policy's text, every selector value reaching
// outside the caller's view - subject and rule peers alike - and keeps what
// the caller can see, actions, ports and direction. The snapshot itself, and
// so every verdict, is untouched.
func TestRedact(t *testing.T) {
	s := New(nil, nil)
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "lockdown"},
		"spec": map[string]any{
			"priority": int64(10),
			"subject":  map[string]any{"namespaces": nameInSelector("team-a", "team-b")},
			"ingress": []any{map[string]any{
				"action": "Allow",
				"from": []any{
					map[string]any{"namespaces": map[string]any{"matchLabels": map[string]any{"dotvirt.io/project": "team-b"}}},
					map[string]any{"namespaces": nameInSelector("team-a", "team-c", "team-d")},
				},
				"ports": []any{map[string]any{"portNumber": map[string]any{"protocol": "TCP", "port": int64(443)}}},
			}},
			"egress": []any{map[string]any{
				"action": "Deny",
				"to":     []any{map[string]any{"namespaces": map[string]any{"matchLabels": map[string]any{"env": "prod"}}}},
			}},
		},
	})
	red := &Redaction{
		ProjectLabel: "dotvirt.io/project",
		Namespace:    func(ns string) bool { return ns == "team-a" },
		Project:      func(p string) bool { return p == "team-a" },
		Namespaces:   universe,
	}
	full := s.Policies()[0]
	leaks := func(text string) bool {
		return strings.Contains(text, "team-b") || strings.Contains(text, "team-c") || strings.Contains(text, "team-d")
	}

	p := full
	p.Rules = slices.Clone(full.Rules)
	s.Redact(&p, nil, red)
	if p.Target != "team-a, other namespaces" {
		t.Errorf("target = %q", p.Target)
	}
	if got := p.Rules[0].Peer; got != "ns other namespaces; ns team-a, other namespaces" {
		t.Errorf("ingress peer = %q", got)
	}
	for i, r := range p.Rules {
		if leaks(r.Peer) {
			t.Errorf("rule %d still names another tenant: %q", i, r.Peer)
		}
		if r.Action != full.Rules[i].Action || r.Ports != full.Rules[i].Ports || r.Direction != full.Rules[i].Direction {
			t.Errorf("rule %d changed beyond its peer: %+v, was %+v", i, r, full.Rules[i])
		}
	}
	if p.Rules[1].Peer != "ns team-a, other namespaces" {
		t.Errorf("a label reaching team-b must read as the caller's namespaces it selects: %q", p.Rules[1].Peer)
	}

	// A trace step: the policy without its table, the matched row apart.
	step, row := full, full.Rules[0]
	step.Rules = nil
	s.Redact(&step, &row, red)
	if step.Rules != nil || leaks(row.Peer) || leaks(step.Target) || row.Ports != "TCP/443" {
		t.Errorf("step = %+v, row = %+v", step, row)
	}

	gone := model.Policy{Name: "deleted", Backing: "AdminNetworkPolicy", Target: "team-b"}
	goneRow := model.PolicyRuleView{Peer: "ns team-b"}
	s.Redact(&gone, &goneRow, red)
	if gone.Target != otherNamespaces || goneRow.Peer != otherNamespaces {
		t.Errorf("a policy the snapshot lost kept its text: %+v %+v", gone, goneRow)
	}

	if again := s.Policies()[0]; again.Target != full.Target || !slices.Equal(again.Rules, full.Rules) {
		t.Error("redaction reached the snapshot")
	}
	if full.Target != "team-a, team-b" {
		t.Errorf("unredacted target = %q", full.Target)
	}
}

// A step Trace emits maps back to its policy row, so Redact rewrites that row
// rather than hiding it whole - and the verdict is the one Trace computed.
func TestRedactTraceStep(t *testing.T) {
	s := New(nil, nil)
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "guard"},
		"spec": map[string]any{
			"priority": int64(10),
			"subject":  map[string]any{"namespaces": nameInSelector("team-a")},
			"ingress": []any{map[string]any{
				"action": "Deny",
				"from":   []any{map[string]any{"namespaces": nameInSelector("team-a", "team-b")}},
			}},
		},
	})
	src := wl("team-b", "client", nil, map[string]string{"app": "client"})
	dst := wl("team-a", "web", nil, map[string]string{"app": "web"})
	res := s.Trace(nil, src, &dst, "", "TCP", 443)
	if res.Verdict != "Deny" {
		t.Fatalf("verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	red := &Redaction{
		Namespace: func(ns string) bool { return ns == "team-a" },
		Project:   func(string) bool { return false },
	}
	found := false
	for i := range res.Steps {
		st := &res.Steps[i]
		if st.Policy == nil || st.Policy.Name != "guard" || st.Rule == nil {
			continue
		}
		found = true
		s.Redact(st.Policy, st.Rule, red)
		if st.Rule.Peer != "ns team-a, other namespaces" || st.Rule.Action != "Deny" || st.Rule.Direction != "Ingress" {
			t.Errorf("step row = %+v, want the row rewritten in place", *st.Rule)
		}
	}
	if !found {
		t.Fatalf("no step for guard: %v", stepKinds(t, res))
	}
}

// ownScope seeds cluster rules whose selectors reach past tenant-a (project
// alpha) into tenant-b (project beta) and kube-system, whose labels the
// snapshot does not hold. secrets are the values only outside namespaces
// explain; clear are the ones confined to tenant-a.
func ownScope(t *testing.T) (s *Snapshot, nsLabels func(string) map[string]string, all []NamespaceLabels, secrets, clear []string) {
	s = New(nil, nil)
	labels := map[string]map[string]string{
		"tenant-a": {"kubernetes.io/metadata.name": "tenant-a", "dotvirt.io/project": "alpha", "env": "prod", "zone": "z1"},
		"tenant-b": {"kubernetes.io/metadata.name": "tenant-b", "dotvirt.io/project": "beta", "env": "prod", "tier": "gold"},
	}
	nsLabels = func(ns string) map[string]string { return labels[ns] }
	all = []NamespaceLabels{{Name: "tenant-a", Labels: labels["tenant-a"]}, {Name: "tenant-b", Labels: labels["tenant-b"]}, {Name: "kube-system"}}
	sel := func(kv ...string) map[string]any {
		m := map[string]any{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return map[string]any{"matchLabels": m}
	}
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "platform"},
		"spec": map[string]any{
			"priority": int64(10),
			"subject":  map[string]any{"namespaces": sel("env", "prod")},
			"ingress": []any{
				map[string]any{"action": "Allow", "from": []any{map[string]any{"pods": map[string]any{
					"namespaceSelector": sel("tier", "gold"), "podSelector": sel("app", "billing"),
				}}}, "ports": []any{map[string]any{"portNumber": map[string]any{"protocol": "TCP", "port": int64(443)}}}},
				map[string]any{"action": "Deny", "from": []any{map[string]any{"namespaces": map[string]any{"matchExpressions": []any{
					map[string]any{"key": "tier", "operator": "NotIn", "values": []any{"bronze"}},
				}}}}},
			},
			"egress": []any{
				map[string]any{"action": "Deny", "to": []any{map[string]any{"nodes": sel("node-role.kubernetes.io/infra", "")}}},
				map[string]any{"action": "Allow", "to": []any{map[string]any{"pods": map[string]any{
					"namespaceSelector": sel("dotvirt.io/project", "alpha"), "podSelector": sel("app", "web"),
				}}}},
			},
		},
	})
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "into-gold"},
		"spec": map[string]any{
			"priority": int64(20),
			"subject":  map[string]any{"namespaces": sel("tier", "gold")},
			"ingress":  []any{map[string]any{"action": "Allow", "from": []any{map[string]any{"namespaces": sel("zone", "z1")}}}},
		},
	})
	add(t, s.banp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "BaselineAdminNetworkPolicy",
		"metadata": map[string]any{"name": "default"},
		"spec": map[string]any{
			"subject": map[string]any{"namespaces": map[string]any{}},
			"ingress": []any{map[string]any{"action": "Deny", "from": []any{map[string]any{"namespaces": sel("dotvirt.io/project", "beta")}}}},
		},
	})
	add(t, s.egressip, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP",
		"metadata": map[string]any{"name": "pool"},
		"spec":     map[string]any{"egressIPs": []any{"192.0.2.10"}, "namespaceSelector": sel("env", "prod"), "podSelector": sel("app", "billing")},
	})
	secrets = []string{"tenant-b", "kube-system", "beta", "gold", "tier", "bronze", "billing", "infra", "zone", "z1", "env=prod"}
	clear = []string{"dotvirt.io/project=alpha", "app=web"}
	return s, nsLabels, all, secrets, clear
}

func policyTexts(p model.Policy, rule *model.PolicyRuleView) []string {
	out := []string{p.Target, p.Applies}
	for _, r := range p.Rules {
		out = append(out, r.Peer, r.Text)
	}
	if rule != nil {
		out = append(out, rule.Peer, rule.Text)
	}
	return out
}

// A tenant reads a cluster rule that governs its VMs on every surface that
// renders one - the policy rows, the effective chain, a network's rules, a
// trace's steps - and never a selector value reaching outside its own
// namespaces. A value confined to them stays in clear.
func TestRedactOwnScopeEverySurface(t *testing.T) {
	s, nsLabels, all, secrets, clear := ownScope(t)
	tenant := &Redaction{
		ProjectLabel: "dotvirt.io/project",
		Namespace:    func(ns string) bool { return ns == "tenant-a" },
		Project:      func(p string) bool { return p == "alpha" },
		Namespaces:   func() []NamespaceLabels { return all },
	}
	var seen []string
	check := func(where string, texts ...string) {
		t.Helper()
		for _, x := range texts {
			for _, secret := range secrets {
				if strings.Contains(x, secret) {
					t.Errorf("%s leaks %q: %q", where, secret, x)
				}
			}
		}
		seen = append(seen, texts...)
	}
	redact := func(where string, p *model.Policy, rule *model.PolicyRuleView) {
		t.Helper()
		if p.Namespace == "" {
			s.Redact(p, rule, tenant)
		}
		check(where, policyTexts(*p, rule)...)
	}

	for _, p := range s.Policies() {
		redact("policy "+p.Name, &p, nil)
	}
	eff := s.Effective("tenant-a", nsLabels("tenant-a"), map[string]string{"app": "web"}, true)
	for _, list := range [][]model.PolicyBinding{eff.EastWest, eff.SNAT} {
		for i := range list {
			redact("effective "+list[i].Policy.Name, &list[i].Policy, nil)
		}
	}
	rules := s.NetworkRules([]string{"tenant-a"}, nsLabels, tenant, "dotvirt.io/project")
	if len(rules.Reaches) == 0 {
		t.Fatal("into-gold must name tenant-a's VMs as its peer")
	}
	for i := range rules.Applies {
		redact("network binding "+rules.Applies[i].Policy.Name, &rules.Applies[i].Policy, nil)
	}
	for i := range rules.Reaches {
		r := &rules.Reaches[i]
		check("peer sentence "+r.Policy.Name, r.Text)
		redact("peer rule "+r.Policy.Name, &r.Policy, &r.Rule)
	}
	src := wl("tenant-a", "web", nsLabels("tenant-a"), map[string]string{"app": "web"})
	dst := wl("tenant-b", "billing", nsLabels("tenant-b"), map[string]string{"app": "billing"})
	res := s.Trace(nil, src, &dst, "", "TCP", 443)
	steps := 0
	for i := range res.Steps {
		if st := &res.Steps[i]; st.Policy != nil {
			steps++
			redact("trace step "+st.Policy.Name, st.Policy, st.Rule)
		}
	}
	if steps == 0 || res.Verdict != "Deny" {
		t.Fatalf("trace = %s with %d policy steps (%v), want platform's deny", res.Verdict, steps, stepKinds(t, res))
	}
	joined := strings.Join(seen, "\n")
	for _, want := range clear {
		if !strings.Contains(joined, want) {
			t.Errorf("a value confined to tenant-a was hidden: %q missing", want)
		}
	}

	var platform model.Policy
	for _, p := range s.Policies() {
		if p.Name == "platform" {
			platform = p
		}
	}
	s.Redact(&platform, nil, tenant)
	if platform.Applies != "every VM in tenant-a, other namespaces" || platform.Target != "tenant-a, other namespaces" {
		t.Errorf("subject reads %q / %q, want the caller's namespace named", platform.Applies, platform.Target)
	}
	want := []string{
		"ns other namespaces pods selected", "Accepts TCP 443 from selected VMs in other namespaces.",
		"ns tenant-a, other namespaces", "Blocks all traffic from VMs in tenant-a, other namespaces.",
		"nodes selected", "Cannot reach selected hosts.",
		"ns dotvirt.io/project=alpha pods app=web", "Can reach VMs labeled app=web in namespaces labeled dotvirt.io/project=alpha.",
	}
	if got := policyTexts(platform, nil)[2:]; !slices.Equal(got, want) {
		t.Errorf("rows = %q\nwant %q", got, want)
	}
}

// No Redaction - a caller who may list the kind - reads every
// cluster rule exactly as the policy plane renders it. A caller who sees
// every namespace and may read nodes reads all of it but a value no
// namespace carries: platform text no namespace of theirs explains.
func TestRedactAdminSeesAll(t *testing.T) {
	s, _, all, _, _ := ownScope(t)
	everywhere := &Redaction{
		ProjectLabel: "dotvirt.io/project",
		Namespace:    func(string) bool { return true },
		Project:      func(string) bool { return true },
		Namespaces:   func() []NamespaceLabels { return all },
		Nodes:        func() bool { return true },
	}
	for _, full := range s.Policies() {
		for _, red := range []*Redaction{nil, everywhere} {
			p := full
			p.Rules = slices.Clone(full.Rules)
			s.Redact(&p, nil, red)
			got, want := policyTexts(p, nil), policyTexts(full, nil)
			if red != nil && full.Name == "platform" {
				want[4], want[5] = "ns tenant-a, tenant-b, other namespaces", "Blocks all traffic from VMs in tenant-a, tenant-b, other namespaces."
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s through %v = %q, want %q", full.Name, red != nil, got, want)
			}
		}
	}
}

// A namespace selector is confined only when nothing outside the caller's
// view can match it: a project-label selector proves it even past namespaces
// whose labels the snapshot lacks, a bare label never does there, and with no
// namespace list only a name pin does.
func TestRedactionConfinement(t *testing.T) {
	_, _, all, _, _ := ownScope(t)
	red := &Redaction{
		ProjectLabel: "dotvirt.io/project",
		Namespace:    func(ns string) bool { return ns == "tenant-a" },
		Project:      func(p string) bool { return p == "alpha" },
		Namespaces:   func() []NamespaceLabels { return all },
	}
	ls := func(kv ...string) *metav1.LabelSelector {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return &metav1.LabelSelector{MatchLabels: m}
	}
	names := func(ns ...string) *metav1.LabelSelector {
		return &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpIn, Values: ns}}}
	}
	for _, c := range []struct {
		name string
		sel  *metav1.LabelSelector
		want bool
	}{
		{"project of the caller", ls("dotvirt.io/project", "alpha"), true},
		{"project and a label", ls("dotvirt.io/project", "alpha", "zone", "z1"), true},
		{"another project", ls("dotvirt.io/project", "beta"), false},
		{"a label only tenant-a carries, past unknown labels", ls("zone", "z1"), false},
		{"a label tenant-b carries", ls("env", "prod"), false},
		{"named namespaces", names("tenant-a"), true},
		{"named past the view", names("tenant-a", "tenant-b"), false},
		{"every namespace", &metav1.LabelSelector{}, false},
		{"a project with no namespace", ls("dotvirt.io/project", "gamma"), false},
		{"a label no namespace carries", ls("team", "finance"), false},
		{"named, but narrowed to nothing", &metav1.LabelSelector{
			MatchLabels:      map[string]string{"team": "finance"},
			MatchExpressions: names("tenant-a").MatchExpressions,
		}, false},
	} {
		if got := red.confined(parseSelector(c.sel)); got != c.want {
			t.Errorf("%s: confined = %v, want %v", c.name, got, c.want)
		}
	}

	blind := *red
	blind.Namespaces, blind.memo = nil, nil
	if !blind.confined(parseSelector(names("tenant-a"))) || blind.confined(parseSelector(ls("dotvirt.io/project", "alpha"))) {
		t.Error("with no namespace list only a name pin may confine")
	}

	// Every selector the library rejects shares one memo key, so each must
	// read from its own requirements.
	rejected := func(ns string) selector {
		return parseSelector(&metav1.LabelSelector{
			MatchLabels:      map[string]string{"kubernetes.io/metadata.name": ns},
			MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "env", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"not a value!"}}},
		})
	}
	mine, theirs := rejected("tenant-a"), rejected("tenant-b")
	if mine.err == nil || theirs.err == nil {
		t.Fatal("a selector with an invalid value must be rejected")
	}
	if got := red.read(theirs).own; got != otherNamespaces {
		t.Errorf("rejected selector on tenant-b reads %q, want %q", got, otherNamespaces)
	}
	if got, want := red.read(mine).own, "tenant-a, "+otherNamespaces; got != want {
		t.Errorf("rejected selector on tenant-a reads %q, want %q", got, want)
	}
}

// A selector matching none of the caller's namespaces, nothing at all
// included, is the platform's own text: it reads as other namespaces, its
// pod labels hidden beside it. Without a Redaction it reads whole.
func TestRedactVacuousSelector(t *testing.T) {
	s, _, all, _, _ := ownScope(t)
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "finance"},
		"spec": map[string]any{
			"priority": int64(30),
			"subject":  map[string]any{"namespaces": map[string]any{"matchLabels": map[string]any{"team": "finance"}}},
			"ingress": []any{map[string]any{"action": "Deny", "from": []any{
				map[string]any{"namespaces": map[string]any{"matchLabels": map[string]any{"dotvirt.io/project": "gamma"}}},
				map[string]any{"pods": map[string]any{
					"namespaceSelector": map[string]any{
						"matchLabels":      map[string]any{"team": "finance"},
						"matchExpressions": nameInSelector("tenant-a")["matchExpressions"],
					},
					"podSelector": map[string]any{"matchLabels": map[string]any{"app": "ledger"}},
				}},
			}}},
		},
	})
	tenant := &Redaction{
		ProjectLabel: "dotvirt.io/project",
		Namespace:    func(ns string) bool { return ns == "tenant-a" },
		Project:      func(p string) bool { return p == "alpha" },
		Namespaces:   func() []NamespaceLabels { return all },
	}
	var full model.Policy
	for _, p := range s.Policies() {
		if p.Name == "finance" {
			full = p
		}
	}
	p := full
	p.Rules = slices.Clone(full.Rules)
	s.Redact(&p, nil, tenant)
	for _, x := range policyTexts(p, nil) {
		for _, secret := range []string{"finance", "gamma", "ledger"} {
			if strings.Contains(x, secret) {
				t.Errorf("a vacuous selector leaks %q: %q", secret, x)
			}
		}
	}
	if p.Target != otherNamespaces || p.Rules[0].Peer != "ns other namespaces; ns other namespaces pods selected" {
		t.Errorf("target %q, peer %q", p.Target, p.Rules[0].Peer)
	}
	whole := full
	s.Redact(&whole, nil, nil)
	if !strings.Contains(whole.Target, "team=finance") || !strings.Contains(whole.Rules[0].Peer, "app=ledger") {
		t.Errorf("without a Redaction the rule reads whole: %q %q", whole.Target, whole.Rules[0].Peer)
	}
}

// A selector confined to the caller's namespaces still renders in clear only
// with values those namespaces carry: a NotIn or In value only another
// tenant's namespace explains reads as other namespaces.
func TestRedactConfinedSpellsOnlyCarriedValues(t *testing.T) {
	s := New(nil, nil)
	add(t, s.anp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "AdminNetworkPolicy",
		"metadata": map[string]any{"name": "x"},
		"spec": map[string]any{
			"priority": int64(10),
			"subject": map[string]any{"namespaces": map[string]any{
				"matchLabels":      map[string]any{"dotvirt.io/project": "team-a"},
				"matchExpressions": []any{map[string]any{"key": "customer", "operator": "NotIn", "values": []any{"acme-of-team-b"}}},
			}},
			"ingress": []any{map[string]any{"action": "Allow", "from": []any{
				map[string]any{"namespaces": map[string]any{
					"matchLabels":      map[string]any{"dotvirt.io/project": "team-a"},
					"matchExpressions": []any{map[string]any{"key": "env", "operator": "In", "values": []any{"prod", "secret-env-b"}}},
				}},
				map[string]any{"namespaces": map[string]any{
					"matchLabels":      map[string]any{"dotvirt.io/project": "team-a"},
					"matchExpressions": []any{map[string]any{"key": "env", "operator": "In", "values": []any{"prod"}}},
				}},
			}}},
		},
	})
	red := &Redaction{
		ProjectLabel: "dotvirt.io/project",
		Namespace:    func(ns string) bool { return ns == "team-a" },
		Project:      func(p string) bool { return p == "team-a" },
		Namespaces:   universe,
	}
	p := s.Policies()[0]
	s.Redact(&p, nil, red)
	for _, txt := range policyTexts(p, nil) {
		if strings.Contains(txt, "acme-of-team-b") || strings.Contains(txt, "secret-env-b") {
			t.Errorf("a value no namespace of the caller's carries reads in clear: %q", txt)
		}
	}
	if p.Target != "team-a, other namespaces" || p.Rules[0].Peer != "ns team-a, other namespaces; ns dotvirt.io/project=team-a, env in (prod)" {
		t.Errorf("target %q, peer %q", p.Target, p.Rules[0].Peer)
	}
}

// One request's redaction over a large cluster: the namespace list splits
// once, and a project selector tests only that project's namespaces.
func BenchmarkRedactionRead(b *testing.B) {
	var all []NamespaceLabels
	for i := range 5000 {
		if i%2 == 0 {
			all = append(all, NamespaceLabels{Name: fmt.Sprintf("ns-%d", i), Labels: map[string]string{"dotvirt.io/project": fmt.Sprintf("p%d", i/10), "env": "prod"}})
		} else {
			all = append(all, NamespaceLabels{Name: fmt.Sprintf("ns-%d", i)})
		}
	}
	var sels []selector
	for j := range 50 {
		sels = append(sels, parseSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"dotvirt.io/project": fmt.Sprintf("p%d", j)}}))
	}
	for b.Loop() {
		r := &Redaction{ProjectLabel: "dotvirt.io/project", Namespace: func(ns string) bool { return ns == "ns-0" },
			Project: func(string) bool { return false }, Namespaces: func() []NamespaceLabels { return all }}
		for _, s := range sels {
			r.confined(s)
		}
	}
}
