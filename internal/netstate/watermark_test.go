package netstate

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/cluster"
)

func object(ns, name, rv string) *unstructured.Unstructured {
	md := map[string]any{"name": name, "resourceVersion": rv}
	if ns != "" {
		md["namespace"] = ns
	}
	return &unstructured.Unstructured{Object: map[string]any{"metadata": md}}
}

// sharedOnly admits the shared networks alone, as for a tenant without
// authority over any cluster-tier kind; everything admits them all.
func sharedOnly(gvr schema.GroupVersionResource) bool { return gvr == gvrCUDN }
func everything(schema.GroupVersionResource) bool     { return true }

// A caller's watermark moves with the catalog it can see - its namespaces'
// objects and the cluster-scoped kinds it is admitted to - and never with
// another tenant's namespace. Node-level detail moves only a node reader's.
func TestWatermarkScopesToTheCaller(t *testing.T) {
	s := New(nil, nil)
	_ = s.nad.Add(object("team-a", "net", "1"))
	_ = s.netpol.Add(object("team-b", "deny", "1"))
	_ = s.cudn.Add(object("", "shared", "1"))
	_ = s.anp.Add(object("", "lockdown", "1"))
	_ = s.nns.Add(object("", "worker-1", "1"))
	s.nodes = []cluster.NodeLabels{{Name: "worker-1", Labels: map[string]string{"zone": "a"}}}

	a := map[string]bool{"team-a": true}
	tenant, reader := s.Watermark(a, sharedOnly, false), s.Watermark(a, everything, true)
	check := func(what string, tenantMoves, readerMoves bool) {
		t.Helper()
		gotT, gotR := s.Watermark(a, sharedOnly, false), s.Watermark(a, everything, true)
		if (gotT != tenant) != tenantMoves || (gotR != reader) != readerMoves {
			t.Errorf("%s: tenant moved %v, node reader moved %v; want %v, %v", what, gotT != tenant, gotR != reader, tenantMoves, readerMoves)
		}
		tenant, reader = gotT, gotR
	}

	_ = s.netpol.Update(object("team-b", "deny", "2"))
	_ = s.nad.Add(object("team-b", "other", "1"))
	check("another tenant's objects", false, false)

	_ = s.nad.Update(object("team-a", "net", "2"))
	check("an own object", true, true)

	_ = s.cudn.Update(object("", "shared", "2"))
	check("a shared object", true, true)

	_ = s.anp.Update(object("", "lockdown", "2"))
	check("an admin policy only its authority sees", false, true)

	_ = s.nns.Update(object("", "worker-1", "2"))
	check("node state churn", false, false)

	_ = s.nns.Add(object("", "worker-2", "1"))
	check("a host reporting node state", false, true)

	s.nodes = []cluster.NodeLabels{{Name: "worker-1", Labels: map[string]string{"zone": "b"}}}
	check("a node relabelled", false, true)

	if s.Watermark(map[string]bool{"team-a": true, "team-b": true}, sharedOnly, false) == tenant {
		t.Error("gaining a namespace must move the watermark")
	}
}

// BenchmarkWatermark is the per-frame catalog watermark over 1000 namespaced
// objects in 50 namespaces plus 50 shared ones: a tenant seeing 5 namespaces,
// and an admin seeing all 50 who also reads 10 nodes of 30 labels each.
func BenchmarkWatermark(b *testing.B) {
	s := New(nil, nil)
	stores := []interface{ Add(any) error }{s.udn, s.nad, s.netpol, s.egressfw}
	for i := range 1000 {
		_ = stores[i%len(stores)].Add(object(fmt.Sprintf("ns-%d", i%50), fmt.Sprintf("o-%d", i), "123456"))
	}
	for i := range 50 {
		_ = s.cudn.Add(object("", fmt.Sprintf("c-%d", i), "123456"))
	}
	for i := range 10 {
		labels := map[string]string{}
		for l := range 30 {
			labels[fmt.Sprintf("example.io/label-%d", l)] = "value"
		}
		name := fmt.Sprintf("worker-%d", i)
		s.nodes = append(s.nodes, cluster.NodeLabels{Name: name, Labels: labels})
		_ = s.nns.Add(object("", name, "1"))
	}
	scope := func(n int) map[string]bool {
		visible := map[string]bool{}
		for i := range n {
			visible[fmt.Sprintf("ns-%d", i)] = true
		}
		return visible
	}
	b.Run("tenant", func(b *testing.B) {
		visible := scope(5)
		b.ReportAllocs()
		for b.Loop() {
			_ = s.Watermark(visible, sharedOnly, false)
		}
	})
	b.Run("admin", func(b *testing.B) {
		visible := scope(50)
		b.ReportAllocs()
		for b.Loop() {
			_ = s.Watermark(visible, everything, true)
		}
	})
}
