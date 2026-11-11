package netstate

import (
	"encoding/json"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// isolated seeds tenant-a on a primary UDN, tenant-b on another, tenant-c
// and tenant-d on one primary CUDN, and a default network whose node
// subnets the node LIST reports (one node per annotation form), in a cluster
// that declares its pod and service networks and serves no route
// advertisement. podNet false leaves the default network's range unknown.
func isolated(t *testing.T, podNet bool) *Snapshot {
	t.Helper()
	s := New(nil, nil)
	s.routeAds.Store(routeAdsAbsent)
	s.clusterNet, s.svcNet = parsePrefixes([]string{"10.128.0.0/14"}), parsePrefixes([]string{"172.30.0.0/16"})
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
		"metadata": map[string]any{"name": "shared", "uid": "x"},
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
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": ns + "/" + name},
		"spec":     spec,
	})
	add(t, s.nad, map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{
			"name": name, "namespace": ns,
			"ownerReferences": []any{map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork", "name": name, "uid": ns + "/" + name}},
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

// seeing resolves the addresses of vms, as the caller's view does for the
// VMs in namespaces they see.
func seeing(vms ...TraceWorkload) *TraceView {
	return &TraceView{
		NADs: func(string) bool { return true },
		UDNs: func(string) bool { return true },
		Workloads: func(addr netip.Addr) []TraceWorkload {
			var out []TraceWorkload
			for _, w := range vms {
				if slices.Contains(w.IPs, addr.String()) {
					out = append(out, w)
				}
			}
			return out
		},
	}
}

// One network spanning namespaces is one network: an address in its range,
// or in the source's own (a default-network VM the caller sees), takes the
// path it always did, and so does every address in a cluster with the
// default network alone.
func TestTraceSameNetworkUnchanged(t *testing.T) {
	s := isolated(t, true)
	peers := seeing(wl("plain", "db", nil, nil, "10.128.0.7"), wl("tenant-d", "peer", nil, nil, "10.50.0.9"))
	for name, c := range map[string]struct {
		src TraceWorkload
		ip  string
	}{
		"same CUDN, other namespace": {wl("tenant-c", "app", nil, nil, "10.50.0.4"), "10.50.0.9"},
		"default network to itself":  {wl("plain", "legacy", nil, nil, "10.128.0.9"), "10.128.0.7"},
	} {
		res := s.Trace(peers, c.src, nil, c.ip, "TCP", 443)
		if st := connectivityStep(t, res); res.Verdict != "Allow" || st.Action != "Reachable" || st.Conditional {
			t.Errorf("%s: verdict %s, step %+v", name, res.Verdict, st)
		}
	}
	alone := New(nil, nil)
	if res := alone.Trace(nil, wl("plain", "legacy", nil, nil), nil, "203.0.113.9", "TCP", 443); res.Verdict != "Allow" {
		t.Errorf("default network alone, outside address: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// An outside address from a primary UDN is certain again once the cluster's
// infrastructure ranges are known: allowed by default, denied by an
// outbound rule.
func TestTraceOutsideAddressFromIsolatedNetwork(t *testing.T) {
	s := isolated(t, true)
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	res := s.Trace(nil, a, nil, "203.0.113.9", "TCP", 443)
	if st := connectivityStep(t, res); res.Verdict != "Allow" || st.Action != "Reachable" || st.Conditional {
		t.Fatalf("outside address: verdict %s, step %+v", res.Verdict, st)
	}
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "tenant-a"},
		"spec":     map[string]any{"egress": []any{map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}}}},
	})
	if res := s.Trace(nil, a, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Deny" {
		t.Errorf("outbound deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// Beside a primary UDN a service address answers across networks only for
// the Kubernetes API (the service network's first address) and DNS (its
// tenth); any other may be an isolated network's service, from a UDN or the
// default network alike, so an Allow, or an outbound rule's Deny, is not
// certain. A source egress rule's Deny is. With no primary UDN a service
// address is reached as before.
func TestTraceServiceAddressFromIsolatedNetwork(t *testing.T) {
	s := isolated(t, true)
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	res := s.Trace(nil, a, nil, "172.30.12.7", "TCP", 8080)
	if st := connectivityStep(t, res); res.Verdict != "Conditional" || !st.Conditional || !strings.Contains(st.Note, "cluster service address") {
		t.Errorf("service address: verdict %s, step %+v", res.Verdict, st)
	}
	for _, ip := range []string{"172.30.0.1", "172.30.0.10"} {
		if res := s.Trace(nil, a, nil, ip, "TCP", 443); res.Verdict != "Allow" {
			t.Errorf("API or DNS service %s: verdict = %s (%v)", ip, res.Verdict, stepKinds(t, res))
		}
	}
	plain := wl("plain", "legacy", nil, nil, "10.128.0.9")
	if res := s.Trace(nil, plain, nil, "172.30.12.7", "TCP", 8080); res.Verdict != "Conditional" {
		t.Errorf("service address from the default network beside UDNs: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	if res := s.Trace(nil, plain, nil, "172.30.0.10", "UDP", 53); res.Verdict != "Allow" {
		t.Errorf("DNS from the default network: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	alone := New(nil, nil)
	alone.svcNet = parsePrefixes([]string{"172.30.0.0/16"})
	if res := alone.Trace(nil, plain, nil, "172.30.12.7", "TCP", 8080); res.Verdict != "Allow" {
		t.Errorf("service address with no primary UDN: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "tenant-a"},
		"spec":     map[string]any{"egress": []any{map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}}}},
	})
	if res := s.Trace(nil, a, nil, "172.30.12.7", "TCP", 8080); res.Verdict != "Conditional" {
		t.Errorf("outbound deny on a service address: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	// OVN-K does not bind an admin policy to a VM Network's pods: its Deny
	// may not apply, so it decides nothing.
	anp(t, s, "no-egress", 1, map[string]any{"action": "Deny", "to": []any{map[string]any{"networks": []any{"0.0.0.0/0"}}}})
	res = s.Trace(nil, a, nil, "172.30.12.7", "TCP", 8080)
	if res.Verdict != "Conditional" {
		t.Errorf("admin deny from a VM Network: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	var named bool
	for _, st := range res.Steps {
		if st.Stage == "admin" && st.Conditional && strings.Contains(st.Note, "VM Network") {
			named = true
		}
	}
	if !named {
		t.Errorf("the admin rule is not a maybe naming the VM Network: %+v", res.Steps)
	}
}

// A range another tenant configures never shapes a verdict: an address in
// one the caller cannot read is traced exactly as an outside address, to the
// byte. A caller who reads that range reads the overlay answer.
func TestTraceForeignRangeReadsAsOutside(t *testing.T) {
	s := isolated(t, true)
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	tenant := &TraceView{NADs: func(ns string) bool { return ns == "tenant-a" }, UDNs: func(ns string) bool { return ns == "tenant-a" }, Shared: false}
	bytesFor := func(view *TraceView, ip string) string {
		t.Helper()
		b, err := json.Marshal(s.Trace(view, a, nil, ip, "TCP", 443))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(b), ip, "<ip>")
	}
	outside := bytesFor(tenant, "203.0.113.9")
	if !strings.Contains(outside, `"verdict":"Allow"`) {
		t.Fatalf("outside address: %s", outside)
	}
	for _, ip := range []string{"10.200.3.4", "10.50.0.9"} {
		if got := bytesFor(tenant, ip); got != outside {
			t.Errorf("%s in a range the tenant cannot read:\n%s\nwant\n%s", ip, got, outside)
		}
	}
	if res := s.Trace(nil, a, nil, "10.200.3.4", "TCP", 443); !strings.Contains(connectivityStep(t, res).Note, "no in-cluster path") {
		t.Errorf("a caller reading tenant-b's range: %+v", connectivityStep(t, res))
	}
}

// Deny only when certain: where an infrastructure range, or the source's
// own, is unknown, the trace says so from a primary UDN instead of calling
// the flow unreachable or allowed. The default network's answers are as
// before.
func TestTraceIsolationMissingRanges(t *testing.T) {
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	plain := wl("plain", "legacy", nil, nil)
	unplaced(t, isolated(t, false), nil, a, "10.128.1.5", "203.0.113.9")
	if res := isolated(t, false).Trace(nil, plain, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Allow" {
		t.Errorf("default network, its range unknown: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	// A node not yet annotated hosts pods in a range nothing reports.
	s := isolated(t, false)
	s.podNet = defaultPodSubnets([]cluster.NodeLabels{{Name: "w1", PodSubnets: `{"default":["10.128.0.0/23"]}`}, {Name: "w3"}})
	unplaced(t, s, nil, a, "10.128.4.5", "10.128.1.5")

	// Node subnets outside the declared pod network disagree with it.
	s = isolated(t, false)
	s.podNet = defaultPodSubnets([]cluster.NodeLabels{{Name: "w1", PodSubnets: `{"default":["10.140.0.0/23"]}`}})
	unplaced(t, s, nil, a, "10.140.0.5", "203.0.113.9")

	// Without the cluster's network config the service range is unknown.
	s = isolated(t, true)
	s.clusterNet, s.svcNet = nil, nil
	unplaced(t, s, nil, a, "203.0.113.9", "172.30.12.7")
	if res := s.Trace(nil, a, nil, "10.128.1.5", "TCP", 443); res.Verdict != "Unreachable" {
		t.Errorf("default range known, service range unknown: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	s = isolated(t, true)
	primaryUDN(t, s, "tenant-e", "bare", map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary"}})
	unplaced(t, s, nil, wl("tenant-e", "vm", nil, nil, "10.9.0.4"), "10.128.1.5", "10.200.3.4")
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
	if res := s.Trace(seeing(wl("plain", "db", nil, nil, "10.128.1.5")), vm, nil, "10.128.1.5", "TCP", 443); res.Verdict != "Allow" {
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

// Infrastructure ranges place an address for every caller; of the ranges
// tenants configure, only the caller's own and the shared ones published to
// them (or all, where their catalog lists all) do.
func TestTraceViewOwnScope(t *testing.T) {
	s := isolated(t, true)
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	sees := func(nss ...string) func(string) bool {
		return func(ns string) bool { return slices.Contains(nss, ns) }
	}
	tenant := &TraceView{NADs: sees("tenant-a"), UDNs: sees("tenant-a"), Shared: false}
	tenant.Workloads = seeing(wl("tenant-a", "db", nil, nil, "10.100.0.7")).Workloads
	if res := s.Trace(tenant, a, nil, "10.100.0.7", "TCP", 443); res.Verdict != "Allow" {
		t.Errorf("own network: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	if res := s.Trace(tenant, a, nil, "10.128.1.5", "TCP", 443); res.Verdict != "Unreachable" {
		t.Errorf("the default network, for any caller: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	for name, view := range map[string]*TraceView{
		"published to them": {NADs: sees("tenant-a", "tenant-c"), Shared: false},
		"every shared one":  {NADs: sees("tenant-a"), Shared: true},
	} {
		res := s.Trace(view, a, nil, "10.50.0.9", "TCP", 443)
		if st := connectivityStep(t, res); !strings.Contains(st.Note, "no in-cluster path") {
			t.Errorf("shared network %s: %+v", name, st)
		}
	}
}

// Beside a primary UDN a default-network pod address may be a UDN VM's
// infrastructure interface, which OVN-K isolates: certain only for a
// default-network VM the caller sees. With no primary UDN it is as before.
func TestTraceDefaultPodBesideUDN(t *testing.T) {
	s := isolated(t, true)
	plain := wl("plain", "legacy", nil, nil, "10.128.0.9")
	res := s.Trace(nil, plain, nil, "10.128.1.77", "TCP", 443)
	if st := connectivityStep(t, res); res.Verdict != "Conditional" || !st.Conditional || !strings.Contains(st.Note, "pod address on the cluster default network") {
		t.Errorf("unseen default-range address: verdict %s, step %+v", res.Verdict, st)
	}
	onUDN := wl("tenant-a", "web", nil, nil, "10.128.1.77")
	if res := s.Trace(seeing(onUDN), plain, nil, "10.128.1.77", "TCP", 443); res.Verdict != "Conditional" {
		t.Errorf("an address a UDN VM holds: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	if res := s.Trace(seeing(wl("plain", "db", nil, nil, "10.128.1.77")), plain, nil, "10.128.1.77", "TCP", 443); res.Verdict != "Allow" {
		t.Errorf("a default-network VM the caller sees: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	alone := New(nil, nil)
	alone.podNet = parsePrefixes([]string{"10.128.0.0/23"})
	if res := alone.Trace(seeing(wl("plain", "db", nil, nil, "10.128.1.77")), plain, nil, "10.128.1.77", "TCP", 443); res.Verdict != "Allow" {
		t.Errorf("no primary UDN: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// The EgressFirewall excludes the cluster's pod subnets, so it never decides
// a flow to a pod address: the walk skips it. A service flow is DNATed into
// those subnets unless its endpoints lie outside, so an outbound rule's Deny
// on a service address is not certain; a source egress rule's is.
func TestTraceOutboundRulesBindOnlyLeavingTraffic(t *testing.T) {
	fwDeny := func(s *Snapshot, ns, cidr string) {
		add(t, s.egressfw, map[string]any{
			"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
			"metadata": map[string]any{"name": "default", "namespace": ns},
			"spec":     map[string]any{"egress": []any{map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": cidr}}}},
		})
	}
	alone := New(nil, nil)
	alone.clusterNet, alone.svcNet = parsePrefixes([]string{"10.128.0.0/14"}), parsePrefixes([]string{"172.30.0.0/16"})
	alone.podNet = parsePrefixes([]string{"10.128.0.0/23"})
	fwDeny(alone, "plain", "0.0.0.0/0")
	plain := wl("plain", "legacy", nil, nil, "10.128.0.9")
	outbound := func(st model.TraceStep) bool { return st.Stage == "gateway" || st.Stage == "snat" }
	for view, want := range map[*TraceView]string{nil: "Conditional", seeing(wl("plain", "db", nil, nil, "10.128.1.77")): "Allow"} {
		res := alone.Trace(view, plain, nil, "10.128.1.77", "TCP", 443)
		if res.Verdict != want || slices.ContainsFunc(res.Steps, outbound) {
			t.Errorf("pod address under an outbound deny: verdict %s, want %s (%v)", res.Verdict, want, stepKinds(t, res))
		}
	}
	for _, ip := range []string{"172.30.12.7", "172.30.0.10"} {
		if res := alone.Trace(nil, plain, nil, ip, "TCP", 443); res.Verdict != "Conditional" {
			t.Errorf("service %s under an outbound deny: verdict = %s (%v)", ip, res.Verdict, stepKinds(t, res))
		}
	}
	if res := alone.Trace(nil, plain, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Deny" {
		t.Errorf("outside address under an outbound deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}

	s := isolated(t, true)
	fwDeny(s, "tenant-a", "172.30.0.0/16")
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	if res := s.Trace(nil, a, nil, "172.30.0.10", "UDP", 53); res.Verdict != "Conditional" {
		t.Errorf("DNS from a UDN under an outbound deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	if res := s.Trace(nil, a, nil, "10.100.0.7", "TCP", 443); slices.ContainsFunc(res.Steps, func(st model.TraceStep) bool { return st.Stage == "gateway" }) {
		t.Errorf("its own network's address runs the outbound rules: %v", stepKinds(t, res))
	}
}

// A stopped default-network VM reports no address: it is on the default
// network while its namespace declares no primary UDN.
func TestTraceStoppedDefaultVM(t *testing.T) {
	s := isolated(t, true)
	if res := s.Trace(nil, wl("plain", "legacy", nil, nil), nil, "8.8.8.8", "TCP", 443); res.Verdict != "Allow" {
		t.Errorf("stopped default VM: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	add(t, s.udn, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
		"metadata": map[string]any{"name": "late", "namespace": "unlabeled"},
		"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary", "subnets": []any{"10.150.0.0/16"}}},
	})
	unplaced(t, s, nil, wl("unlabeled", "vm", nil, nil), "8.8.8.8", "203.0.113.9")
}

// A NAD's owner reference proves its network only with the owner's UID: a
// tenant may write any reference into its own namespace's NAD, and a forged
// one neither realizes a UDN nor publishes a shared network.
func TestTraceForgedOwnerReference(t *testing.T) {
	s := isolated(t, true)
	add(t, s.nad, map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{"name": "shared", "namespace": "tenant-x",
			"ownerReferences": []any{map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork", "name": "shared", "uid": "forged"}}},
	})
	add(t, s.udn, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
		"metadata": map[string]any{"name": "net-y", "namespace": "tenant-y", "uid": "real"},
		"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary", "subnets": []any{"10.160.0.0/16"}}},
	})
	add(t, s.nad, map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{"name": "net-y", "namespace": "tenant-y",
			"ownerReferences": []any{map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork", "name": "net-y", "uid": "forged"}}},
	})
	for _, n := range s.Catalog().Networks {
		if n.Name == "shared" && slices.Contains(n.Namespaces, "tenant-x") {
			t.Errorf("a forged reference published %s to tenant-x: %v", n.Name, n.Namespaces)
		}
	}
	if key, _ := s.primaryDomain("tenant-x"); key != "default" {
		t.Errorf("tenant-x on %s through a forged reference", key)
	}
	if s.realized("tenant-y", "net-y") {
		t.Error("a forged reference realized net-y")
	}
	if key, _ := s.primaryDomain("tenant-c"); key != "cudn:shared" {
		t.Errorf("a proven reference must still publish: tenant-c on %s", key)
	}
}

// A pod address on the source's own network is a VM: traced by address it
// answers as traced by name, its ingress walked, when the caller sees that
// VM; otherwise its ingress is unread and the answer is Conditional, worded
// alike for every such address.
func TestTracePodAddressWalksIngress(t *testing.T) {
	s := New(nil, nil)
	s.routeAds.Store(routeAdsAbsent)
	s.clusterNet, s.svcNet = parsePrefixes([]string{"10.128.0.0/14"}), parsePrefixes([]string{"172.30.0.0/16"})
	s.podNet = parsePrefixes([]string{"10.128.0.0/23"})
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "deny-in", "namespace": "db"},
		"spec":     map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress"}},
	})
	src := wl("plain", "legacy", nil, nil, "10.128.0.9")
	dst := wl("db", "pg", nil, nil, "10.128.1.5")
	byVM := s.Trace(nil, src, &dst, "", "TCP", 5432)
	byIP := s.Trace(seeing(dst), src, nil, "10.128.1.5", "TCP", 5432)
	if byVM.Verdict != "Deny" || byIP.Verdict != byVM.Verdict || !strings.Contains(connectivityStep(t, byIP).Note, "10.128.1.5 is pg in db") {
		t.Errorf("by name %s, by address %s (%v)", byVM.Verdict, byIP.Verdict, stepKinds(t, byIP))
	}
	var want string
	for _, ip := range []string{"10.128.1.5", "10.128.1.6"} {
		res := s.Trace(nil, src, nil, ip, "TCP", 5432)
		note := strings.ReplaceAll(connectivityStep(t, res).Note, ip, "<ip>")
		if want == "" {
			want = note
		}
		if res.Verdict != "Conditional" || note != want || !strings.Contains(note, "ingress rules cannot be read") {
			t.Errorf("unseen pod %s: verdict %s, note %q", ip, res.Verdict, note)
		}
	}
}

// With the cluster's pod range unknown any address may be a pod's, which
// the outbound rules never bind: their Deny is not certain.
func TestTraceOutboundDenyWithPodRangeUnknown(t *testing.T) {
	s := New(nil, nil)
	s.routeAds.Store(routeAdsAbsent)
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "plain"},
		"spec":     map[string]any{"egress": []any{map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "10.128.0.0/14"}}}},
	})
	plain := wl("plain", "legacy", nil, nil, "10.128.0.9")
	if res := s.Trace(nil, plain, nil, "10.128.1.5", "TCP", 443); res.Verdict != "Conditional" {
		t.Errorf("outbound deny, pod range unknown: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	s.clusterNet = parsePrefixes([]string{"10.128.0.0/14"})
	if res := s.Trace(nil, plain, nil, "10.129.0.5", "TCP", 443); res.Verdict == "Deny" {
		t.Errorf("a pod address skips the outbound rules once the pod range is known: %v", stepKinds(t, res))
	}
}

// A UDN range follows the right to list UDNs there; a published CUDN does not,
// as in the catalog.
func TestTraceReadsFollowsTheCatalog(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	udn := primaryRange{key: "udn:a/net", namespaces: []string{"a"}}
	cudn := primaryRange{key: "cudn:net", namespaces: []string{"a"}}
	for name, c := range map[string]struct {
		view *TraceView
		r    primaryRange
		want bool
	}{
		"udn, may list":         {&TraceView{NADs: yes, UDNs: yes}, udn, true},
		"udn, sees but no list": {&TraceView{NADs: yes, UDNs: no}, udn, false},
		"cudn published":        {&TraceView{NADs: yes, UDNs: no}, cudn, true},
		"cudn elsewhere":        {&TraceView{NADs: no, UDNs: yes}, cudn, false},
		"cudn, catalog has all": {&TraceView{NADs: no, Shared: true}, cudn, true},
	} {
		if got := c.view.reads(c.r); got != c.want {
			t.Errorf("%s: reads = %v, want %v", name, got, c.want)
		}
	}
}

// OVN-K binds admin and baseline policies to the cluster network's pods: on a
// VM Network their rules may not apply, so between two VMs there no admin or
// baseline Deny decides, and the namespace's effective chain says why. On the
// cluster's own network nothing changes.
func TestAdminPoliciesMayNotBindAVMNetwork(t *testing.T) {
	s := isolated(t, false)
	anp(t, s, "deny-all", 1, map[string]any{"action": "Deny", "to": []any{map[string]any{"namespaces": map[string]any{}}}})
	add(t, s.banp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "BaselineAdminNetworkPolicy",
		"metadata": map[string]any{"name": "default"},
		"spec": map[string]any{
			"subject": map[string]any{"namespaces": map[string]any{}},
			"egress":  []any{map[string]any{"action": "Deny", "to": []any{map[string]any{"namespaces": map[string]any{}}}}},
		},
	})
	src, dst := wl("tenant-a", "a1", nil, nil, "10.100.0.5"), wl("tenant-a", "a2", nil, nil, "10.100.0.6")
	if res := s.Trace(nil, src, &dst, "", "TCP", 80); res.Verdict == "Deny" {
		t.Errorf("an admin deny between two VMs on a VM Network decided: %v", stepKinds(t, res))
	}
	eff := s.Effective("tenant-a", src.NSLabels, nil, false, true, "")
	if len(eff.EastWest) < 2 {
		t.Fatalf("effective chain on a VM Network: %+v, want the admin and baseline bindings", eff.EastWest)
	}
	for _, b := range eff.EastWest {
		if (b.Policy.Kind == "admin" || b.Policy.Kind == "baseline") && (!b.Conditional || !strings.Contains(b.Note, "VM Network")) {
			t.Errorf("%s %s on a VM Network: conditional %v, note %q", b.Policy.Kind, b.Policy.Name, b.Conditional, b.Note)
		}
	}
	plain := New(nil, nil)
	anp(t, plain, "deny-all", 1, map[string]any{"action": "Deny", "to": []any{map[string]any{"namespaces": map[string]any{}}}})
	p1, p2 := wl("team-a", "w1", nil, nil), wl("team-a", "w2", nil, nil)
	if res := plain.Trace(nil, p1, &p2, "", "TCP", 80); res.Verdict != "Deny" {
		t.Errorf("an admin deny on the cluster's own network: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	for _, b := range plain.Effective("team-a", p1.NSLabels, nil, false, true, "").EastWest {
		if b.Policy.Kind == "admin" && b.Conditional {
			t.Errorf("an admin policy on the cluster's own network read as conditional")
		}
	}
}

// A VM's Multus default binding decides its chain: to a NAD a Primary-role
// network generated, it is on that VM Network, admin rules conditional; to
// any other NAD, one a primary UDN owns under another name among them, it is
// off the cluster network. The namespace's own chain stays certain.
func TestEffectiveReadsAWorkloadsDefaultBinding(t *testing.T) {
	s := New(nil, nil)
	anp(t, s, "deny-all", 1, map[string]any{"action": "Deny", "to": []any{map[string]any{"namespaces": map[string]any{}}}})
	primaryUDN(t, s, "team-a", "net-a", map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary"}})
	admin := func(eff model.EffectivePolicy) model.PolicyBinding {
		t.Helper()
		for _, b := range eff.EastWest {
			if b.Policy.Kind == "admin" {
				return b
			}
		}
		t.Fatalf("no admin binding in %+v", eff.EastWest)
		return model.PolicyBinding{}
	}
	labels, pod := map[string]string{"kubernetes.io/metadata.name": "team-a"}, map[string]string{"app": "vm"}
	if b := admin(s.Effective("team-a", labels, pod, true, false, "team-a/net-a")); !b.Conditional || !strings.Contains(b.Note, "VM Network") {
		t.Errorf("a VM bound to its VM Network: conditional %v, note %q", b.Conditional, b.Note)
	}
	add(t, s.nad, ownedNAD("team-a", "alias", "UserDefinedNetwork", "net-a", "team-a/net-a", ""))
	for _, ref := range []string{"team-a/bridge", "team-a/alias"} {
		if b := admin(s.Effective("team-a", labels, pod, true, false, ref)); !b.Conditional || !strings.Contains(b.Note, "no cluster network attachment") {
			t.Errorf("a VM bound to %s, which OVN-K did not generate for a primary network: conditional %v, note %q", ref, b.Conditional, b.Note)
		}
	}
	b := admin(s.Effective("team-b", map[string]string{"kubernetes.io/metadata.name": "team-b"}, pod, true, true, ""))
	if b.Conditional {
		t.Errorf("a VM on the cluster network read as conditional: %q", b.Note)
	}
}

// A VM with no cluster network attachment - only VLAN or other secondary
// NICs, or a default binding no Primary-role network generated - is filtered by no cluster or project
// rule: every binding in its chain is conditional with that reason, and no
// default deny is claimed for it.
func TestEffectiveOffTheClusterNetwork(t *testing.T) {
	s := New(nil, nil)
	anp(t, s, "deny-all", 1, map[string]any{"action": "Deny", "to": []any{map[string]any{"namespaces": map[string]any{}}}})
	add(t, s.banp, map[string]any{
		"apiVersion": "policy.networking.k8s.io/v1alpha1", "kind": "BaselineAdminNetworkPolicy",
		"metadata": map[string]any{"name": "default"},
		"spec":     map[string]any{"subject": map[string]any{"namespaces": map[string]any{}}},
	})
	add(t, s.netpol, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "deny", "namespace": "team-a"},
		"spec":     map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress"}},
	})
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "team-a"},
		"spec":     map[string]any{"egress": []any{map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}}}},
	})
	add(t, s.nad, map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{"name": "vlan42", "namespace": "team-a"},
		"spec":     map[string]any{"config": `{"type":"ovn-k8s-cni-overlay","topology":"localnet","vlanID":42}`},
	})
	add(t, s.nad, map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{"name": "bridge", "namespace": "team-a"},
		"spec":     map[string]any{"config": `{"cniVersion":"0.3.1","type":"bridge","bridge":"br1"}`},
	})
	secondaryCUDN(t, s, "side", "s")
	add(t, s.nad, map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{
			"name": "side", "namespace": "team-a",
			"ownerReferences": []any{map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "ClusterUserDefinedNetwork", "name": "side", "uid": "s"}},
		},
	})
	primaryUDN(t, s, "team-a", "side-udn", map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Secondary"}})
	labels := map[string]string{"kubernetes.io/metadata.name": "team-a"}
	pod := map[string]string{"app": "vm"}
	for name, eff := range map[string]model.EffectivePolicy{
		"a secondary UDN's default binding":     s.Effective("team-a", labels, pod, true, false, "team-a/side-udn"),
		"VLAN only":                             s.Effective("team-a", labels, pod, true, false, ""),
		"a localnet default binding":            s.Effective("team-a", labels, pod, true, false, "team-a/vlan42"),
		"a raw NAD's default binding":           s.Effective("team-a", labels, pod, true, false, "team-a/bridge"),
		"a secondary network's default binding": s.Effective("team-a", labels, pod, true, false, "team-a/side"),
		"an unread NAD's default binding":       s.Effective("team-a", labels, pod, true, false, "team-a/gone"),
	} {
		if eff.DefaultDenyIngress || eff.DefaultDenyEgress {
			t.Errorf("%s: default deny claimed (%v, %v)", name, eff.DefaultDenyIngress, eff.DefaultDenyEgress)
		}
		all := slices.Concat(eff.EastWest, eff.Gateway, eff.SNAT, eff.Routes)
		if len(all) < 4 {
			t.Fatalf("%s: chain %+v, want the admin, project, baseline and firewall bindings", name, all)
		}
		for _, b := range all {
			if !b.Conditional || !strings.Contains(b.Note, "no cluster network attachment") {
				t.Errorf("%s: %s %s conditional %v, note %q", name, b.Policy.Kind, b.Policy.Name, b.Conditional, b.Note)
			}
		}
	}
	on := s.Effective("team-a", labels, pod, true, true, "")
	if !on.DefaultDenyIngress {
		t.Error("a VM on the cluster network lost its default deny")
	}
	for _, b := range on.EastWest {
		if b.Policy.Kind == "admin" && b.Conditional {
			t.Errorf("a VM on the cluster network read admin rules as conditional: %q", b.Note)
		}
	}
}
