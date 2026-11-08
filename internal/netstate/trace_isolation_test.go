package netstate

import (
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

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
	add(t, s.udn, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
		"metadata": map[string]any{"name": "net-a", "namespace": "tenant-a"},
		"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary", "subnets": []any{"10.100.0.0/16"}}},
	})
	add(t, s.udn, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
		"metadata": map[string]any{"name": "net-b", "namespace": "tenant-b"},
		"spec": map[string]any{"topology": "Layer3", "layer3": map[string]any{"role": "Primary", "subnets": []any{
			map[string]any{"cidr": "10.200.0.0/16", "hostSubnet": int64(24)},
		}}},
	})
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

// A primary network is isolated from the default network and from every
// other primary network: an address in their ranges is no path, never the
// cluster egress path an outside address takes.
func TestTraceIsolatedNetworks(t *testing.T) {
	s := isolated(t, true)
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	for _, c := range []struct {
		name, ip, holder string
	}{
		{"primary UDN to a default network pod", "10.128.1.5", "the cluster default network"},
		{"primary UDN to a default network pod on another node", "10.128.3.5", "the cluster default network"},
		{"primary UDN to another primary UDN", "10.200.3.4", "another network"},
		{"primary UDN to a primary CUDN it does not span", "10.50.0.9", "another network"},
	} {
		res := s.Trace(a, nil, c.ip, "TCP", 443)
		st := connectivityStep(t, res)
		if res.Verdict != "Unreachable" || st.Action != "Unreachable" || !st.Decisive || !strings.Contains(st.Note, c.holder) {
			t.Errorf("%s: verdict %s, step %+v", c.name, res.Verdict, st)
		}
		if strings.Contains(st.Note, "tenant-b") || strings.Contains(st.Note, "net-b") || strings.Contains(st.Note, "shared") {
			t.Errorf("%s: the note names a network the caller may not see: %q", c.name, st.Note)
		}
		for _, other := range res.Steps {
			if other.Stage == "admin" || other.Stage == "gateway" || other.Stage == "default" {
				t.Errorf("%s: no policy runs on a path that does not exist: %v", c.name, stepKinds(t, res))
			}
		}
	}

	plain := wl("plain", "legacy", nil, nil, "10.128.0.9")
	if res := s.Trace(plain, nil, "10.100.0.7", "TCP", 443); res.Verdict != "Unreachable" {
		t.Errorf("default network to a primary UDN: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// One network spanning namespaces is one network: an address in its range,
// or in the source's own, takes the path it always did.
func TestTraceSameNetworkUnchanged(t *testing.T) {
	s := isolated(t, true)
	c := wl("tenant-c", "app", nil, nil, "10.50.0.4")
	for name, ip := range map[string]string{
		"same CUDN, other namespace": "10.50.0.9",
		"default network to itself":  "10.128.0.7",
	} {
		src := c
		if strings.HasPrefix(ip, "10.128.") {
			src = wl("plain", "legacy", nil, nil, "10.128.0.9")
		}
		res := s.Trace(src, nil, ip, "TCP", 443)
		if st := connectivityStep(t, res); res.Verdict != "Allow" || st.Action != "Reachable" || st.Conditional {
			t.Errorf("%s: verdict %s, step %+v", name, res.Verdict, st)
		}
	}
}

// An outside address from a primary UDN keeps its verdict: allowed by
// default, denied by an outbound rule.
func TestTraceExternalFromIsolatedNetwork(t *testing.T) {
	s := isolated(t, true)
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	res := s.Trace(a, nil, "203.0.113.9", "TCP", 443)
	if st := connectivityStep(t, res); res.Verdict != "Allow" || st.Action != "Reachable" || st.Conditional {
		t.Fatalf("outside address: verdict %s, step %+v", res.Verdict, st)
	}
	add(t, s.egressfw, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "EgressFirewall",
		"metadata": map[string]any{"name": "default", "namespace": "tenant-a"},
		"spec":     map[string]any{"egress": []any{map[string]any{"type": "Deny", "to": map[string]any{"cidrSelector": "0.0.0.0/0"}}}},
	})
	if res := s.Trace(a, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Deny" {
		t.Errorf("outbound deny: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
}

// Deny only when certain: where a range the answer turns on is unknown, the
// trace says so instead of calling the flow unreachable or allowed.
func TestTraceIsolationMissingRanges(t *testing.T) {
	s := isolated(t, false)
	a := wl("tenant-a", "web", nil, nil, "10.100.0.5")
	for _, ip := range []string{"10.128.1.5", "203.0.113.9"} {
		res := s.Trace(a, nil, ip, "TCP", 443)
		if st := connectivityStep(t, res); res.Verdict != "Conditional" || !st.Conditional || !strings.Contains(st.Note, "unknown here") {
			t.Errorf("default range unknown, %s: verdict %s, step %+v", ip, res.Verdict, st)
		}
	}

	s = isolated(t, true)
	add(t, s.udn, map[string]any{
		"apiVersion": "k8s.ovn.org/v1", "kind": "UserDefinedNetwork",
		"metadata": map[string]any{"name": "bare", "namespace": "tenant-e"},
		"spec":     map[string]any{"topology": "Layer2", "layer2": map[string]any{"role": "Primary"}},
	})
	e := wl("tenant-e", "vm", nil, nil)
	if res := s.Trace(e, nil, "10.200.3.4", "TCP", 443); res.Verdict != "Conditional" {
		t.Errorf("own range unknown, another network's address: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	if res := s.Trace(a, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Conditional" {
		t.Errorf("another primary network's range unknown, an address in no known range: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
	}
	// Whatever the unknown range holds, it is not the source's own network.
	if res := s.Trace(a, nil, "10.128.1.5", "TCP", 443); res.Verdict != "Unreachable" {
		t.Errorf("another network's address beside an unknown range: verdict = %s (%v)", res.Verdict, stepKinds(t, res))
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
		res := s.Trace(a, nil, "10.128.1.5", "TCP", 443)
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
		for _, ip := range []string{"10.128.1.5", "10.200.3.4"} {
			res := s.Trace(a, nil, ip, "TCP", 443)
			st := connectivityStep(t, res)
			if res.Verdict != "Conditional" || st.Action != "Unreachable" || st.Decisive || !st.Conditional ||
				!strings.Contains(st.Note, "Route advertisement may join") {
				t.Errorf("state %d, %s: verdict %s, step %+v", state, ip, res.Verdict, st)
			}
			if strings.Contains(st.Note, "tenant-b") || strings.Contains(st.Note, "net-b") {
				t.Errorf("state %d, %s: the note names the other network: %q", state, ip, st.Note)
			}
		}
		b := wl("tenant-b", "db", nil, nil, "10.200.3.4")
		if res := s.Trace(a, &b, "", "TCP", 5432); res.Verdict != "Conditional" || !strings.Contains(connectivityStep(t, res).Note, "Route advertisement") {
			t.Errorf("state %d, east-west: verdict %s (%v)", state, res.Verdict, stepKinds(t, res))
		}
		if res := s.Trace(a, nil, "203.0.113.9", "TCP", 443); res.Verdict != "Allow" {
			t.Errorf("state %d, outside address: verdict = %s (%v)", state, res.Verdict, stepKinds(t, res))
		}
	}
}

// Discovery alone decides the route-advertisement state, off the request
// path: served sticks, absent re-probes, a failed probe keeps the last answer.
func TestProbeRouteAds(t *testing.T) {
	kube := fake.NewSimpleClientset()
	disc := kube.Discovery().(*fakediscovery.FakeDiscovery)
	s := New(cluster.NewClient(kube, nil, nil), nil)

	if s.probeRouteAds() || s.routeAds.Load() != routeAdsAbsent {
		t.Fatalf("unserved API: state %d", s.routeAds.Load())
	}
	kube.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("discovery down")
	})
	if s.probeRouteAds() || s.routeAds.Load() != routeAdsAbsent {
		t.Fatalf("a failed probe must keep the last answer: state %d", s.routeAds.Load())
	}
	kube.ReactionChain = kube.ReactionChain[1:]
	disc.Resources = []*metav1.APIResourceList{{
		GroupVersion: gvrRouteAds.GroupVersion().String(),
		APIResources: []metav1.APIResource{{Name: gvrRouteAds.Resource}},
	}}
	if !s.probeRouteAds() || s.routeAds.Load() != routeAdsServed {
		t.Fatalf("served API: state %d", s.routeAds.Load())
	}
}
