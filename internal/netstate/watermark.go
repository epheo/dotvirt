package netstate

import (
	"hash/maphash"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/reflect"
)

// Watermark fingerprints the slice of the catalog one caller can see, so the
// caller re-pulls it when that slice moves and only then: another tenant's
// network change never moves this caller's frame. The slice is every
// signalling kind's objects in the visible namespaces, the cluster-scoped
// objects of the kinds sees admits (the caller's authority decides which), and
// nmstate's presence; with nodes, also what only node readers see: the node
// set uplinks derive from and which hosts report a NodeNetworkState (by
// membership only, as its silent watch signals). The stores' per-namespace
// write counts stand in for their objects, so the cost follows the visible
// namespaces, never the catalog's size.
func (s *Snapshot) Watermark(visible map[string]bool, sees func(schema.GroupVersionResource) bool, lists func(schema.GroupVersionResource, string) bool, nodes bool) uint64 {
	scoped := make([]string, 0, len(visible)+1)
	var sum uint64
	for _, w := range s.watches {
		switch {
		case w.signal:
			scoped = scoped[:0]
			for ns := range visible {
				if lists(w.gvr, ns) {
					scoped = append(scoped, ns)
				}
			}
			if sees(w.gvr) {
				scoped = append(scoped, "")
			}
			sum += reflect.Generation(w.idx, scoped...)
		case nodes:
			for _, key := range w.idx.ListKeys() {
				sum += digest(w.gvr.Resource, key)
			}
		}
	}
	if s.nmstatePresent.Load() {
		sum += digest("nmstate")
	}
	if nodes {
		s.nodesMu.RLock()
		for _, n := range s.nodes {
			sum += digest("node", n.Name)
			for k, v := range n.Labels {
				sum += digest("label", n.Name, k, v)
			}
		}
		s.nodesMu.RUnlock()
	}
	return sum
}

// digestSeed keys watermarks, which are compared within one process only.
var digestSeed = maphash.MakeSeed()

func digest(parts ...string) uint64 {
	var h maphash.Hash
	h.SetSeed(digestSeed)
	for _, p := range parts {
		_, _ = h.WriteString(p)
		_ = h.WriteByte(0)
	}
	return h.Sum64()
}
