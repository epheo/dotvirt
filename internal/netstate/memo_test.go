package netstate

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/cluster"
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

// freshCopy is a snapshot never read, holding what s holds.
func freshCopy(t testing.TB, s *Snapshot) *Snapshot {
	t.Helper()
	f := New(nil, nil)
	for i, w := range s.watches {
		for _, obj := range w.idx.List() {
			if err := f.watches[i].idx.Add(obj); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.setNodes(s.nodes)
	f.clusterNet, f.svcNet = s.clusterNet, s.svcNet
	f.nmstatePresent.Store(s.nmstatePresent.Load())
	f.routeAds.Store(s.routeAds.Load())
	return f
}

// A move is one write a watch or the node LIST makes; each moves one source.
type move struct {
	name string
	do   func(s *Snapshot, round int) error
}

// edit rewrites the stored object at key through fn, as an update event: a
// new copy at the next resourceVersion.
func edit(idx cache.Indexer, key string, fn func(obj map[string]any) error) error {
	got, ok, _ := idx.GetByKey(key)
	if !ok {
		return fmt.Errorf("no %s", key)
	}
	u := got.(*unstructured.Unstructured).DeepCopy()
	if err := fn(u.Object); err != nil {
		return err
	}
	rv, _ := strconv.Atoi(u.GetResourceVersion())
	u.SetResourceVersion(strconv.Itoa(rv + 1))
	return idx.Update(u)
}

var policyMoves = []move{
	{"anp reprioritized", func(s *Snapshot, round int) error {
		return edit(s.anp, "tier-03", func(o map[string]any) error {
			return unstructured.SetNestedField(o, int64(90+round*20), "spec", "priority")
		})
	}},
	{"netpol deleted", func(s *Snapshot, round int) error {
		gone, ok, _ := s.netpol.GetByKey(scaleNS(4+round%2) + "/egress-db")
		if !ok {
			return nil
		}
		return s.netpol.Delete(gone)
	}},
	{"netpol added", func(s *Snapshot, round int) error {
		return s.netpol.Add(&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
			"metadata": map[string]any{"name": fmt.Sprintf("added-%d", round%2), "namespace": scaleNS(4)},
			"spec":     map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Egress"}},
		}})
	}},
	{"baseline flipped", func(s *Snapshot, round int) error {
		return edit(s.banp, "default", func(o map[string]any) error {
			action := []string{"Allow", "Deny"}[round%2]
			return unstructured.SetNestedSlice(o, []any{map[string]any{"action": action, "from": []any{map[string]any{"namespaces": map[string]any{}}}}}, "spec", "ingress")
		})
	}},
	{"firewall edited", func(s *Snapshot, round int) error {
		return edit(s.egressfw, scaleNS(5)+"/default", func(o map[string]any) error {
			return unstructured.SetNestedSlice(o, []any{map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": fmt.Sprintf("203.0.%d.0/24", 113+round)}}}, "spec", "egress")
		})
	}},
	{"pool renumbered", func(s *Snapshot, round int) error {
		return edit(s.egressip, "pool-00", func(o map[string]any) error {
			return unstructured.SetNestedStringSlice(o, []string{fmt.Sprintf("192.0.2.%d", 100+round)}, "spec", "egressIPs")
		})
	}},
	{"route moved", func(s *Snapshot, round int) error {
		return edit(s.extroute, "route-00", func(o map[string]any) error {
			return unstructured.SetNestedSlice(o, []any{map[string]any{"ip": fmt.Sprintf("198.51.100.%d", 100+round)}}, "spec", "nextHops", "static")
		})
	}},
}

var networkMoves = []move{
	{"udn declared", func(s *Snapshot, round int) error {
		ns := scaleNS(3000 + round)
		return s.udn.Add(&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
			"metadata": map[string]any{"name": "vm-net", "namespace": ns, "uid": "udn-" + ns},
			"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary", "subnets": []any{fmt.Sprintf("10.77.%d.0/24", round)}}},
		}})
	}},
	{"udn realized", func(s *Snapshot, round int) error {
		ns := scaleNS(3000 + round)
		return s.nad.Add(&unstructured.Unstructured{Object: ownedNAD(ns, "vm-net", "UserDefinedNetwork", "vm-net", "udn-"+ns, `{"topology":"layer2"}`)})
	}},
	{"cudn renumbered", func(s *Snapshot, round int) error {
		return edit(s.cudn, "shared-000", func(o map[string]any) error {
			return unstructured.SetNestedStringSlice(o, []string{fmt.Sprintf("10.128.%d.0/22", 4*round)}, "spec", "network", "layer2", "subnets")
		})
	}},
	{"cudn nad gone", func(s *Snapshot, round int) error {
		gone, ok, _ := s.nad.GetByKey(scaleNS(round) + "/shared-000")
		if !ok {
			return nil
		}
		return s.nad.Delete(gone)
	}},
	{"host nic moved", func(s *Snapshot, round int) error {
		return edit(s.nns, "worker-01", func(o map[string]any) error {
			ifaces, _, _ := unstructured.NestedSlice(o, "status", "currentState", "interfaces")
			ifaces[0].(map[string]any)["state"] = fmt.Sprintf("down-%d", round)
			return unstructured.SetNestedSlice(o, ifaces, "status", "currentState", "interfaces")
		})
	}},
	{"enactment failed", func(s *Snapshot, round int) error {
		return s.nnce.Add(&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "nmstate.io/v1beta1", "kind": "NodeNetworkConfigurationEnactment",
			"metadata": map[string]any{"name": "worker-02.uplink-0",
				"labels": map[string]any{"nmstate.io/policy": "uplink-0", "nmstate.io/node": "worker-02"}},
			"status": map[string]any{"conditions": []any{
				map[string]any{"type": "Failing", "status": "True", "reason": "FailedToConfigure", "message": fmt.Sprintf("round %d", round)},
			}},
		}})
	}},
	{"uplink rebridged", func(s *Snapshot, round int) error {
		return edit(s.nncp, "uplink-0", func(o map[string]any) error {
			return unstructured.SetNestedSlice(o, []any{map[string]any{"localnet": "physnet-0", "bridge": fmt.Sprintf("br-%d", round)}}, "spec", "desiredState", "ovn", "bridge-mappings")
		})
	}},
	{"node added", func(s *Snapshot, round int) error {
		s.nodesMu.RLock()
		nodes := slices.Clone(s.nodes)
		s.nodesMu.RUnlock()
		s.setNodes(append(nodes, cluster.NodeLabels{Name: fmt.Sprintf("extra-%d", round), Labels: map[string]string{"zone": "a"}}))
		return nil
	}},
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

// networkViews renders every view the network memos back.
func networkViews(t testing.TB, s *Snapshot) map[string]string {
	t.Helper()
	view := &TraceView{Namespace: func(ns string) bool { return ns < scaleNS(20) }}
	src := scaleWorkload(150, "10.128.0.10", scaleNS(150)+"/shared-007")
	dst := scaleWorkload(151, "10.128.0.11", scaleNS(151)+"/shared-007")
	views := map[string]string{
		"catalog":     asJSON(t, s.Catalog()),
		"trace":       asJSON(t, s.Trace(nil, src, &dst, "", "TCP", 443)),
		"isolated":    asJSON(t, s.Trace(view, scaleWorkload(10, "10.64.10.10", scaleNS(10)+"/shared-000"), nil, "10.128.4.20", "TCP", 443)),
		"overlay":     asJSON(t, s.Trace(view, scaleWorkload(150, "10.128.0.10"), nil, "10.64.12.7", "TCP", 443)),
		"cudn-domain": asJSON(t, s.Trace(view, scaleWorkload(1005, "10.90.0.5"), nil, "203.0.113.5", "TCP", 443)),
	}
	for _, ns := range []string{scaleNS(5), scaleNS(1005), scaleNS(3000), scaleNS(3001), scaleNS(4999)} {
		key, display := s.primaryDomain(ns)
		views["primary "+ns] = fmt.Sprint(key, display, s.declaresPrimaryUDN(ns))
	}
	return views
}

// matchFresh applies each move in turn and, after every one, compares what s
// answers, read many times, to what a snapshot never read answers, byte for
// byte.
func matchFresh(t *testing.T, moves []move, views func(*Snapshot) map[string]string) {
	s := scaleSnapshot(t)
	compare := func(after string) {
		t.Helper()
		_ = views(s) // a second read must serve the same answer
		got, want := views(s), views(freshCopy(t, s))
		for view := range want {
			if got[view] != want[view] {
				t.Errorf("after %s, %s: memoized answer differs from a fresh decode:\n got %s\nwant %s", after, view, got[view], want[view])
			}
		}
	}
	compare("nothing")
	for round := range 2 {
		for _, m := range moves {
			if err := m.do(s, round); err != nil {
				t.Fatalf("%s: %v", m.name, err)
			}
			compare(fmt.Sprintf("%s (round %d)", m.name, round))
		}
	}
}

func TestPolicyViewsMatchFreshDecode(t *testing.T) {
	all := scaleUniverse()
	matchFresh(t, policyMoves, func(s *Snapshot) map[string]string { return policyViews(t, s, all) })
}

func TestNetworkViewsMatchFreshDecode(t *testing.T) {
	matchFresh(t, networkMoves, func(s *Snapshot) map[string]string { return networkViews(t, s) })
	if empty := asJSON(t, New(nil, nil).Catalog()); !strings.Contains(empty, `"networks":[]`) ||
		!strings.Contains(empty, `"uplinks":[{`) || !strings.Contains(empty, `"physicalAdapters":[]`) {
		t.Errorf("empty catalog = %s, want every list present", empty)
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
	// Renumbered, the pool no longer holds the step's row.
	if err := edit(s.egressip, "pool-00", func(o map[string]any) error {
		return unstructured.SetNestedStringSlice(o, []string{"192.0.2.250"}, "spec", "egressIPs")
	}); err != nil {
		t.Fatal(err)
	}
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

// A caller setting fields on its catalog, as /api/networks does on uplinks,
// leaves the next caller's untouched.
func TestCatalogListsAreTheCallers(t *testing.T) {
	s := scaleSnapshot(t)
	before := asJSON(t, s.Catalog())
	mine := s.Catalog()
	for i := range mine.Uplinks {
		mine.Uplinks[i].SourceFile, mine.Uplinks[i].Sync = "uplinks.yaml", "OutOfSync"
	}
	for i := range mine.Networks {
		mine.Networks[i].Namespaces, mine.Networks[i].Sync = nil, "OutOfSync"
	}
	mine.PhysicalAdapters[0].Role = "rewritten"
	if after := asJSON(t, s.Catalog()); after != before {
		t.Errorf("one caller's edits reached the next:\n got %s\nwant %s", after, before)
	}
}

// Readers modify what they are handed as the api does while every source
// moves: under -race, a write into a view another reader holds fails the
// test.
func TestMemoConcurrentReaders(t *testing.T) {
	s := scaleSnapshot(t)
	all := scaleUniverse()
	moved := make(chan struct{})
	go func() {
		defer close(moved)
		for round := range 4 {
			for _, m := range slices.Concat(policyMoves, networkMoves) {
				if err := m.do(s, round); err != nil {
					t.Errorf("%s: %v", m.name, err)
					return
				}
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
				inv := s.Catalog()
				for i := range inv.Uplinks {
					inv.Uplinks[i].SourceFile, inv.Uplinks[i].Sync = "uplinks.yaml", "Synced"
				}
				for i := range inv.Networks {
					inv.Networks[i].Sync = "Synced"
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
				dst := scaleWorkload(151, "10.128.0.11")
				ew := s.Trace(nil, scaleWorkload(150, "10.128.0.10"), &dst, "", "TCP", 443)
				rules := s.NetworkRules([]string{scaleNS(0), scaleNS(1)}, scaleLabelsOf(), scaleTenant(all), "dotvirt.io/project")
				_ = asJSON(t, []any{inv, pols, eff, res, ew, rules, networkViews(t, s)})
			}
		})
	}
	readers.Wait()
}

// A relist that changed nothing hands every object over as a new copy; the
// next real move still decodes only the object that moved.
func TestPlaneReuseSurvivesRelist(t *testing.T) {
	s := scaleSnapshot(t)
	decodes := 0
	st := newPolicyStore(s.netpol, func(u *unstructured.Unstructured) *policy { decodes++; return decodeNetpol(u) }, nil)
	st.plane()
	var copies []any
	for _, obj := range s.netpol.List() {
		copies = append(copies, obj.(*unstructured.Unstructured).DeepCopy())
	}
	if err := s.netpol.Replace(copies, "2"); err != nil {
		t.Fatal(err)
	}
	if err := edit(s.netpol, scaleNS(4)+"/allow-web", func(map[string]any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	decodes = 0
	st.plane()
	if decodes != 1 {
		t.Errorf("one move after a relist decoded %d policies, want 1", decodes)
	}
}

// A store whose writes go uncounted would freeze a view memoized on it.
func TestMemoRefusesUncountedStore(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a policy store over an uncounted indexer was built")
		}
	}()
	newPolicyStore(cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{}), decodeNetpol, nil)
}
