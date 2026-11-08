package netstate

import (
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// isolated seeds tenant-a on a primary UDN, tenant-b on another, tenant-c
// and tenant-d on one primary CUDN, and a default network whose node
// subnets the node LIST reports (one node per annotation form), in a cluster
// that serves no route advertisement. podNet false leaves that range unknown.
func isolated(t *testing.T, podNet bool) *Snapshot {
	t.Helper()
	s := New(nil, nil)
	s.routeAds.Store(routeAdsAbsent)
	if podNet {
		s.podNet = defaultPodSubnets([]cluster.NodeLabels{
			{Name: "w1", PodSubnets: `{"default":["10.128.0.0/23"]}`},
			{Name: "w2", PodSubnets: `{"default":"10.128.2.0/23"}`},
		})
	}
	primaryUDN(t, s, "tenant-a", "net-a", map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary", "subnets": []any{"10.100.0.0/16"}}})
	primaryUDN(t, s, "tenant-b", "net-b", map[string]any{"topology": "Layer3", "layer3": map[string]any{"role": "Primary", "subnets": []any{
		map[string]any{"cidr": "10.200.0.0/16", "hostSubnet": int64(24)},
	}}})
	add(t, s.cudn, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork",
		"metadata": map[string]any{"name": "shared"},
		"spec": map[string]any{
			"namespaceSelector": nameInSelector("tenant-c", "tenant-d"),
			"network":           map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary", "subnets": []any{"10.50.0.0/16"}}},
		},
	})
	for _, ns := range []string{"tenant-c", "tenant-d"} {
		add(t, s.nad, map[string]any{
			"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
			"metadata": map[string]any{
				"name": "shared", "namespace": ns,
				"ownerReferences": []any{map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork", "name": "shared", "uid": "x"}},
			},
		})
	}
	return s
}

// primaryUDN adds a UDN and the NAD OVN-K generates for it once realized.
func primaryUDN(t *testing.T, s *Snapshot, ns, name string, spec map[string]any) {
	t.Helper()
	add(t, s.udn, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec":     spec,
	})
	add(t, s.nad, map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{
			"name": name, "namespace": ns,
			"ownerReferences": []any{map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork", "name": name, "uid": "y"}},
		},
	})
}

func connectivityStep(t *testing.T, res model.TraceResult) model.TraceStep {
	t.Helper()
	for _, st := range res.Steps {
		if st.Stage == "connectivity" {
			return st
		}
	}
	t.Fatalf("no connectivity step: %v", stepKinds(t, res))
	return model.TraceStep{}
}

// unplaced asserts the answer for an address the caller's view cannot place:
// Conditional, the step worded alike for every such address.
func unplaced(t *testing.T, s *Snapshot, view *TraceView, src TraceWorkload, ips ...string) {
	t.Helper()
	var want string
	for _, ip := range ips {
		res := s.Trace(view, src, nil, ip, "TCP", 443)
		st := connectivityStep(t, res)
		note := strings.ReplaceAll(st.Note, ip, "<ip>")
		if want == "" {
			want = note
		}
		if res.Verdict != "Conditional" || !st.Conditional || note != want || !strings.Contains(note, "cannot be ruled out") {
			t.Errorf("%s: verdict %s, note %q, want the note %q", ip, res.Verdict, st.Note, want)
		}
	}
}

// From a primary UDN the default network's range is no path: certain, with
// the isolation as the reason, from the ranges the caller reads.
func TestTraceIsolatedFromDefaultNetwork(t *testing.T) {
	s := isolated(t, true)
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	for _, ip := range []string{"10.128.1.5", "10.128.3.5"} {
		res := s.Trace(nil, a, nil, ip, "TCP", 443)
		st := connectivityStep(t, res)
		if res.Verdict != "Unreachable" || st.Action != "Unreachable" || !st.Decisive || !strings.Contains(st.Note, "the cluster default network") {
			t.Errorf("%s: verdict %s, step %+v", ip, res.Verdict, st)
		}
		for _, other := range res.Steps {
			if other.Stage == "admin" || other.Stage == "gateway" || other.Stage == "default" {
				t.Errorf("%s: no policy runs on a path that does not exist: %v", ip, stepKinds(t, res))
			}
		}
	}
}

// A UDN or CUDN range is overlay only and chosen by its tenant: an address in
// it is no in-cluster path, yet the gateway still carries the flow to that
// address outside the cluster. Never Unreachable, never naming the network.
func TestTraceOverlayRangeIsNeverUnreachable(t *testing.T) {
	s := isolated(t, true)
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	plain := wl("plain", "legacy", nil, nil, "10.128.0.9")
	for _, c := range []struct {
		name string
		src  TraceWorkload
		ip   string
	}{
		{"primary UDN to another primary UDN", a, "10.200.3.4"},
		{"primary UDN to a primary CUDN it does not span", a, "10.50.0.9"},
		{"default network to a primary UDN", plain, "10.200.9.9"},
	} {
		res := s.Trace(nil, c.src, nil, c.ip, "TCP", 443)
		st := connectivityStep(t, res)
		if res.Verdict != "Conditional" || !st.Conditional || !strings.Contains(st.Note, "no in-cluster path") ||
			!strings.Contains(st.Note, "also exists outside the cluster") {
			t.Errorf("%s: verdict %s, step %+v", c.name, res.Verdict, st)
		}
		for _, name := range []string{"tenant-b", "net-b", "shared"} {
			if strings.Contains(st.Note, name) {
				t.Errorf("%s: the note names %q: %q", c.name, name, st.Note)
			}
		}
	}
}

// One network spanning namespaces is one network: an address in its range,
// or in the source's own, takes the path it always did, and so does every
// address in a cluster with the default network alone.
func TestTraceSameNetworkUnchanged(t *testing.T) {
	s := isolated(t, true)
	for name, c := range map[string]struct {
		src TraceWorkload
		ip  string
	}{
		"same CUDN, other namespace": {wl("tenant-c", "app", nil, nil, "10.50.0.4"), "10.50.0.9"},
		"default network to itself":  {wl("plain", "legacy", nil, nil, "10.128.0.9"), "10.128.0.7"},
	} {
		res := s.Trace(nil, c.src, nil, c.ip, "TCP", 443)
		if st := connectivityStep(t, res); res.Verdict != "Allow" || st.Action != "Reachable" || st.Conditional {
			t.Errorf("%s: verdict %s, step %+v", name, res.Verdict, st)
		}
	}
	alone := New(nil, nil)
	if res := alone.Trace(nil, wl("plain", "legacy", nil, nil), nil, "203.0.113.9", "TCP", 443); res.Verdict != "Allow" {
		t.Errorf("default network alone, outside address: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// Beside other primary networks an address in no range read may be a
// service of one of them - from a UDN, a default-network ClusterIP answers
// only for the API and DNS - so an Allow is not certain, worded alike for
// every such address. A source egress rule's Deny stays certain; an outbound
// rule's does not, binding only a flow that leaves the cluster.
func TestTraceUnplacedAddress(t *testing.T) {
	s := isolated(t, true)
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	unplaced(t, s, nil, a, "203.0.113.9", "172.30.12.7")

	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "tenant-a"},
		"spec":     map[string]any{"egress": []any{map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}}}},
	})
	if res := s.Trace(nil, a, nil, "172.30.12.7", "TCP", 443); res.Verdict != "Conditional" {
		t.Errorf("outbound deny on an unplaced address: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	anp(t, s, "no-egress", 1, map[string]any{"action": "Deny", "to": []any{map[string]any{"networks": []any{"0.0.0.0/0"}}}})
	if res := s.Trace(nil, a, nil, "172.30.12.7", "TCP", 443); res.Verdict != "Deny" {
		t.Errorf("source egress deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// Deny only when certain: where a range the answer turns on is unknown, the
// trace says so instead of calling the flow unreachable or allowed.
func TestTraceIsolationMissingRanges(t *testing.T) {
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	unplaced(t, isolated(t, false), nil, a, "10.128.1.5", "203.0.113.9")

	// A node not yet annotated hosts pods in a range nothing reports.
	s := isolated(t, false)
	s.podNet = defaultPodSubnets([]cluster.NodeLabels{{Name: "w1", PodSubnets: `{"default":["10.128.0.0/23"]}`}, {Name: "w3"}})
	unplaced(t, s, nil, a, "10.128.4.5", "10.128.1.5")

	s = isolated(t, true)
	primaryUDN(t, s, "tenant-e", "bare", map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary"}})
	unplaced(t, s, nil, wl("tenant-e", "vm", nil, nil, "10.9.0.4"), "10.128.1.5", "10.200.3.4")
	// Whatever the unknown range holds, it is not the source's own network.
	if res := s.Trace(nil, a, nil, "10.128.1.5", "TCP", 443); res.Verdict != "Unreachable" {
		t.Errorf("the default network's address beside an unknown range: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// A source counts as on its namespace's primary UDN only once OVN-K realized
// it there (an unlabeled namespace keeps the default network), and only when
// one of its addresses lies in that network's range.
func TestTraceSourceOnItsNetwork(t *testing.T) {
	s := isolated(t, true)
	add(t, s.udn, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
		"metadata": map[string]any{"name": "late", "namespace": "unlabeled"},
		"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary", "subnets": []any{"10.150.0.0/16"}}},
	})
	vm := wl("unlabeled", "vm", nil, nil, "10.128.0.20")
	if res := s.Trace(nil, vm, nil, "10.128.1.5", "TCP", 443); res.Verdict != "Allow" {
		t.Errorf("unrealized UDN, default network peer: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	peer := wl("plain", "legacy", nil, nil, "10.128.0.9")
	if res := s.Trace(nil, vm, &peer, "", "TCP", 443); connectivityStep(t, res).Action != "Reachable" {
		t.Errorf("unrealized UDN, east-west to the default network: %v", stepKinds(t, res))
	}

	for name, ips := range map[string][]string{"an address off its range": {"10.128.0.21"}, "a stopped VM": nil} {
		t.Run(name, func(t *testing.T) {
			unplaced(t, s, nil, wl("tenant-a", "web", nil, nil, ips...), "10.128.1.5", "10.100.0.7")
		})
	}
}

// A NIC beside the primary one may still carry the flow: a VLAN to the
// fabric, or a secondary network whose range may hold the address.
func TestTraceIsolationBypass(t *testing.T) {
	s := isolated(t, true)
	add(t, s.nad, map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{"name": "vlan10", "namespace": "tenant-a"},
		"spec":     map[string]any{"config": `{"name":"vlan10","topology":"localnet","physicalNetworkName":"dc","vlanID":10}`},
	})
	add(t, s.nad, map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{"name": "storage", "namespace": "tenant-a"},
		"spec":     map[string]any{"config": `{"name":"storage","topology":"layer2","subnets":"192.168.10.0/24"}`},
	})
	for _, c := range []struct {
		name string
		nets []string
		want string
	}{
		{"a VLAN NIC", []string{"tenant-a/vlan10"}, "Conditional"},
		{"a secondary NIC of unknown range", []string{"tenant-a/scratch"}, "Conditional"},
		{"a secondary NIC elsewhere", []string{"tenant-a/storage"}, "Unreachable"},
	} {
		a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
		a.Nets = c.nets
		res := s.Trace(nil, a, nil, "10.128.1.5", "TCP", 443)
		if res.Verdict != c.want {
			t.Errorf("%s: verdict = %s, want %s (%v)", c.name, res.Verdict, c.want, stepKinds(t, res))
		}
		if st := connectivityStep(t, res); st.Action != "Unreachable" || st.Decisive != (c.want == "Unreachable") {
			t.Errorf("%s: step %+v", c.name, st)
		}
	}
}

// Isolated primaries are certainly apart only where no route advertisement
// can join them: with the API served, or before discovery has said, the
// answer is Conditional, and still never names the other network.
func TestTraceIsolationRouteAdvertisement(t *testing.T) {
	for _, state := range []int32{routeAdsServed, routeAdsUnknown} {
		s := isolated(t, true)
		s.routeAds.Store(state)
		a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
		res := s.Trace(nil, a, nil, "10.128.1.5", "TCP", 443)
		st := connectivityStep(t, res)
		if res.Verdict != "Conditional" || st.Action != "Unreachable" || st.Decisive || !st.Conditional ||
			!strings.Contains(st.Note, "Route advertisement may join") {
			t.Errorf("state %d: verdict %s, step %+v", state, res.Verdict, st)
		}
		b := wl("tenant-b", "db", nil, nil, "10.200.3.4")
		res = s.Trace(nil, a, &b, "", "TCP", 5432)
		if st := connectivityStep(t, res); res.Verdict != "Conditional" || !strings.Contains(st.Note, "Route advertisement") {
			t.Errorf("state %d, east-west: verdict %s (%v)", state, res.Verdict, stepKinds(t, res))
		}
	}
}

// The trace places an address only against the ranges the caller may read:
// their own namespaces' networks, the shared ones published to them, the
// default network's with nodes. Every other range reads as one unknown, the
// same for every address, so no verdict tells a tenant another's range.
func TestTraceViewOwnScope(t *testing.T) {
	s := isolated(t, true)
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	sees := func(nss ...string) func(string) bool {
		return func(ns string) bool { return slices.Contains(nss, ns) }
	}
	no := func() bool { return false }
	yes := func() bool { return true }
	tenant := &TraceView{Namespace: sees("tenant-a"), Shared: no, Nodes: no}
	unplaced(t, s, tenant, a, "10.128.1.5", "10.200.3.4", "10.50.0.9", "203.0.113.9")

	if res := s.Trace(tenant, a, nil, "10.100.0.7", "TCP", 443); res.Verdict != "Allow" {
		t.Errorf("own network: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	withNodes := &TraceView{Namespace: sees("tenant-a"), Shared: no, Nodes: yes}
	if res := s.Trace(withNodes, a, nil, "10.128.1.5", "TCP", 443); res.Verdict != "Unreachable" {
		t.Errorf("default range read with nodes: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	for name, view := range map[string]*TraceView{
		"published to them": {Namespace: sees("tenant-a", "tenant-c"), Shared: no, Nodes: no},
		"every shared one":  {Namespace: sees("tenant-a"), Shared: yes, Nodes: no},
	} {
		res := s.Trace(view, a, nil, "10.50.0.9", "TCP", 443)
		if st := connectivityStep(t, res); !strings.Contains(st.Note, "no in-cluster path") {
			t.Errorf("shared network %s: %+v", name, st)
		}
	}
}
