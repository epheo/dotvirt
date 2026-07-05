// Package netstate is dotvirt's networking read plane: an SA-maintained, watch-fed
// snapshot of the port-group CRDs (UDN/CUDN/NAD) and the physical fabric (nmstate
// NNS/NNCP), following the clusterstate/argo/desched reflector model — Catalog() is a
// pure in-memory scan, never a per-request cluster call.
//
// Port-group moves publish NetworkChanged so the hub re-broadcasts and the client
// re-pulls the out-of-band catalog; NNS (node-state) is watched but does NOT signal —
// its status churns and adapters need no live repaint. Each reflector is discovery-
// gated (like desched): a cluster without OVN-K UDN or nmstate simply serves an empty
// slice for that source, never an error loop. Node names come from a background-
// refreshed cache (nodes:list, no watch), so uplink membership stays off the request
// path without a nodes:watch grant.
package netstate

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/reflect"
)

var (
	gvrUDN  = schema.GroupVersionResource{Group: "k8s.ovn.org", Version: "v1", Resource: "userdefinednetworks"}
	gvrCUDN = schema.GroupVersionResource{Group: "k8s.ovn.org", Version: "v1", Resource: "clusteruserdefinednetworks"}
	gvrNAD  = schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}
	gvrNNS  = schema.GroupVersionResource{Group: "nmstate.io", Version: "v1beta1", Resource: "nodenetworkstates"}
	gvrNNCP = schema.GroupVersionResource{Group: "nmstate.io", Version: "v1", Resource: "nodenetworkconfigurationpolicies"}
)

// Snapshot holds the watch-fed networking stores. Build with New, start with Run;
// Catalog is safe for concurrent callers.
type Snapshot struct {
	sa  *cluster.Client
	bus *eventbus.Bus

	udn, cudn, nad, nns, nncp cache.Indexer
	nmstatePresent            atomic.Bool // the NNS CRD is served (nmstate installed)

	// One health flag per reflector (reflect.TrackHealth): false while that watch
	// errors. Healthy ANDs them, so one broken watch can't be masked by another
	// re-establishing. A CRD still absent (never discovered) stays true — absence is
	// a feature state, not staleness.
	healthy [5]atomic.Bool

	nodesMu sync.RWMutex
	nodes   []cluster.NodeInfo
}

// New builds the snapshot over sa (dotvirt's ServiceAccount client). bus may be nil in
// tests (Catalog reads the stores directly; only the change signal is suppressed).
func New(sa *cluster.Client, bus *eventbus.Bus) *Snapshot {
	idx := func() cache.Indexer { return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{}) }
	s := &Snapshot{
		sa: sa, bus: bus,
		udn: idx(), cudn: idx(), nad: idx(), nns: idx(), nncp: idx(),
	}
	for i := range s.healthy {
		s.healthy[i].Store(true) // optimistic until a list/watch actually errors
	}
	return s
}

// Healthy reports whether every started networking watch is currently established.
// The catalog keeps serving its last-good stores while unhealthy; the inventory
// surfaces a "may be stale" warning so a sustained outage isn't silent.
func (s *Snapshot) Healthy() bool {
	for i := range s.healthy {
		if !s.healthy[i].Load() {
			return false
		}
	}
	return true
}

// discoveryInterval paces the API probe while a CRD is absent — one lightweight
// discovery GET per tick, ending once the reflector owns a watch connection.
const discoveryInterval = time.Minute

// nodeRefreshInterval bounds how stale uplink node membership can get; node add/remove
// is rare and this cache is off the request path, so a coarse poll is enough.
const nodeRefreshInterval = 2 * time.Minute

// Run starts one discovery-gated reflector per CRD plus the node-cache refresher, and
// returns immediately; everything stops when ctx is cancelled. Port-group kinds signal
// NetworkChanged; NNS is watched silently.
func (s *Snapshot) Run(ctx context.Context) {
	go s.watch(ctx, gvrUDN, s.udn, &s.healthy[0], true)
	go s.watch(ctx, gvrCUDN, s.cudn, &s.healthy[1], true)
	go s.watch(ctx, gvrNAD, s.nad, &s.healthy[2], true)
	go s.watch(ctx, gvrNNCP, s.nncp, &s.healthy[3], true)
	go s.watch(ctx, gvrNNS, s.nns, &s.healthy[4], false)
	go s.refreshNodes(ctx)
}

// watch runs a discovery-gated reflector for one GVR: it re-probes slowly until the API
// appears, then owns a watch connection for the rest of the process. signal=true fires
// NetworkChanged on every store move; the NNS reflector passes false (its churn must
// not repaint). Serving the NNS CRD flips nmstatePresent, which gates the fabric UI.
func (s *Snapshot) watch(ctx context.Context, gvr schema.GroupVersionResource, idx cache.Indexer, healthy *atomic.Bool, signal bool) {
	t := time.NewTicker(discoveryInterval)
	defer t.Stop()
	for {
		if s.sa.HasAPIResource(gvr) {
			if gvr == gvrNNS {
				s.nmstatePresent.Store(true)
			}
			onChange := func() {}
			if signal && s.bus != nil {
				onChange = func() { s.bus.Publish(eventbus.NetworkChanged) }
			}
			store := reflect.NewStore(idx, onChange, nil)
			lw := reflect.TrackHealth(s.sa.DynamicListWatch(gvr), healthy)
			r := cache.NewReflector(lw, &unstructured.Unstructured{}, store, 0)
			r.Run(ctx.Done()) // blocks until shutdown; owns its own relist/backoff
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refreshNodes keeps the node-name cache current via a periodic LIST (populated at once
// on start, then every nodeRefreshInterval).
func (s *Snapshot) refreshNodes(ctx context.Context) {
	t := time.NewTicker(nodeRefreshInterval)
	defer t.Stop()
	for {
		if infos, err := s.sa.NodeInfos(ctx); err == nil {
			s.nodesMu.Lock()
			s.nodes = infos
			s.nodesMu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// listOf returns the store's objects as typed unstructured pointers; non-unstructured
// entries (there are none in practice) are skipped.
func listOf(idx cache.Indexer) []*unstructured.Unstructured {
	all := idx.List()
	out := make([]*unstructured.Unstructured, 0, len(all))
	for _, obj := range all {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			out = append(out, u)
		}
	}
	return out
}
