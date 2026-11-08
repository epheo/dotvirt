package netstate

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/model"
)

func asJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// churn moves the policy stores as the watches would: an ANP edited, a
// NetworkPolicy deleted and another added, an EgressIP pool renumbered.
func churn(s *Snapshot, round int) error {
	anp, _, _ := s.anp.GetByKey("tier-03")
	u := anp.(*unstructured.Unstructured).DeepCopy()
	if err := unstructured.SetNestedField(u.Object, int64(200+round), "spec", "priority"); err != nil {
		return err
	}
	if err := s.anp.Update(u); err != nil {
		return err
	}
	if gone, ok, _ := s.netpol.GetByKey(scaleNS(4) + "/egress-db"); ok {
		if err := s.netpol.Delete(gone); err != nil {
			return err
		}
	}
	if err := s.netpol.Add(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": fmt.Sprintf("added-%d", round%3), "namespace": scaleNS(4)},
		"spec":     map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Egress"}},
	}}); err != nil {
		return err
	}
	pool, _, _ := s.egressip.GetByKey("pool-00")
	p := pool.(*unstructured.Unstructured).DeepCopy()
	if err := unstructured.SetNestedStringSlice(p.Object, []string{fmt.Sprintf("192.0.2.%d", 100+round)}, "spec", "egressIPs"); err != nil {
		return err
	}
	return s.egressip.Update(p)
}

func mustChurn(t *testing.T, s *Snapshot, round int) {
	t.Helper()
	if err := churn(s, round); err != nil {
		t.Fatal(err)
	}
}

// policyViews renders every view the policy planes back, redacted for a
// tenant where the api redacts.
func policyViews(t testing.TB, s *Snapshot, all []NamespaceLabels) map[string]string {
	t.Helper()
	eff := s.Effective(scaleNS(4), scaleNSLabels(4), map[string]string{"app": "web"}, true)
	s.redactEffective(&eff, scaleTenant(all))
	nss := make([]string, 0, scaleCUDNSpan)
	for k := range scaleCUDNSpan {
		nss = append(nss, scaleNS(k))
	}
	rules := s.NetworkRules(nss, scaleLabelsOf(), scaleTenant(all), "dotvirt.io/project")
	src := scaleWorkload(150, "10.128.0.10", scaleNS(150)+"/shared-007")
	dst := scaleWorkload(151, "10.128.0.11", scaleNS(151)+"/shared-007")
	eastWest := s.Trace(nil, src, &dst, "", "TCP", 443)
	external := s.Trace(nil, scaleWorkload(5, "10.64.5.10"), nil, "203.0.113.5", "TCP", 443)
	for i := range external.Steps {
		if st := &external.Steps[i]; st.Policy != nil && st.Policy.Namespace == "" {
			s.Redact(st.Policy, st.Rule, scaleTenant(all))
		}
	}
	return map[string]string{
		"policies":     asJSON(t, s.Policies()),
		"effective":    asJSON(t, eff),
		"rules":        asJSON(t, rules),
		"trace":        asJSON(t, eastWest),
		"trace-egress": asJSON(t, external),
	}
}

// What a snapshot read many times answers is what a snapshot never read
// answers, byte for byte, across store moves.
func TestPolicyViewsMatchFreshDecode(t *testing.T) {
	all := scaleUniverse()
	read := scaleSnapshot(t)
	for round := range 3 {
		for range 2 {
			_ = policyViews(t, read, all)
		}
		mustChurn(t, read, round)
	}
	fresh := scaleSnapshot(t)
	for round := range 3 {
		mustChurn(t, fresh, round)
	}
	got, want := policyViews(t, read, all), policyViews(t, fresh, all)
	for view := range want {
		if got[view] != want[view] {
			t.Errorf("%s: memoized answer differs from a fresh decode:\n got %s\nwant %s", view, got[view], want[view])
		}
	}
}

// A policy gone between the answer and its redaction hides its rows on the
// caller's copy; another caller's answer, sharing the decoded rows, keeps
// them.
func TestRedactGonePolicyKeepsSharedRows(t *testing.T) {
	s := scaleSnapshot(t)
	all := scaleUniverse()
	eff := func() model.EffectivePolicy {
		return s.Effective(scaleNS(4), scaleNSLabels(4), map[string]string{"app": "web"}, true)
	}
	mine, theirs := eff(), eff()
	before := asJSON(t, theirs)
	gone, _, _ := s.egressip.GetByKey("pool-00")
	if err := s.egressip.Delete(gone); err != nil {
		t.Fatal(err)
	}
	if len(mine.SNAT) == 0 {
		t.Fatal("fixture: no SNAT binding")
	}
	s.Redact(&mine.SNAT[0].Policy, nil, scaleTenant(all))
	if mine.SNAT[0].Policy.Rules[0].Peer != otherNamespaces {
		t.Fatalf("gone policy's row = %+v, want it hidden", mine.SNAT[0].Policy.Rules[0])
	}
	if after := asJSON(t, theirs); after != before {
		t.Errorf("redacting one answer rewrote another's:\n got %s\nwant %s", after, before)
	}
}

// A trace's egress-plane step carries its own copy of the rule row the
// redactor rewrites, never the decoded one.
func TestRedactTraceStepKeepsSharedRow(t *testing.T) {
	s := scaleSnapshot(t)
	all := scaleUniverse()
	trace := func() model.TraceResult {
		return s.Trace(nil, scaleWorkload(5, "10.64.5.10"), nil, "203.0.113.5", "TCP", 443)
	}
	mine, theirs := trace(), trace()
	before := asJSON(t, theirs)
	mustChurn(t, s, 0) // renumbers pool-00: the step's row is no longer one of its rows
	found := false
	for i := range mine.Steps {
		if st := &mine.Steps[i]; st.Stage == "snat" {
			found = true
			s.Redact(st.Policy, st.Rule, scaleTenant(all))
		}
	}
	if !found {
		t.Fatal("fixture: no SNAT step")
	}
	if after := asJSON(t, theirs); after != before {
		t.Errorf("redacting one trace rewrote another's:\n got %s\nwant %s", after, before)
	}
}

// Readers modify what they are handed as the api does while the stores move:
// under -race, a write into a view another reader holds fails the test.
func TestMemoConcurrentReaders(t *testing.T) {
	s := scaleSnapshot(t)
	all := scaleUniverse()
	moved := make(chan struct{})
	go func() {
		defer close(moved)
		for round := range 20 {
			if err := churn(s, round); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for done := false; !done; {
				select {
				case <-moved:
					done = true
				default:
				}
				pols := s.Policies()
				for i := range pols {
					pols[i].Sync, pols[i].SourceFile = "Synced", "policies.yaml"
				}
				eff := s.Effective(scaleNS(4), scaleNSLabels(4), map[string]string{"app": "web"}, true)
				for _, bs := range [][]model.PolicyBinding{eff.EastWest, eff.Gateway, eff.SNAT, eff.Routes} {
					for i := range bs {
						bs[i].Policy.Sync = "OutOfSync"
					}
				}
				s.redactEffective(&eff, scaleTenant(all))
				res := s.Trace(nil, scaleWorkload(5, "10.64.5.10"), nil, "203.0.113.5", "TCP", 443)
				for i := range res.Steps {
					if st := &res.Steps[i]; st.Policy != nil && st.Policy.Namespace == "" {
						s.Redact(st.Policy, st.Rule, scaleTenant(all))
					}
				}
				_ = asJSON(t, []any{pols, eff, res})
			}
		})
	}
	readers.Wait()
}
