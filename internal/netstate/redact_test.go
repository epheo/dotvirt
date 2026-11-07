package netstate

import (
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// Redact hides, in a cluster policy's text, every namespace name and project
// label value outside the caller's view - subject and rule peers alike - and
// keeps what the caller can see, other labels, actions, ports and direction.
// The snapshot itself, and so every verdict, is untouched.
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
	if p.Rules[1].Peer != "ns env=prod" {
		t.Errorf("a label that names no namespace was hidden: %q", p.Rules[1].Peer)
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
