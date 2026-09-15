// Package netstate is dotvirt's networking read plane: an SA-maintained, watch-fed
// snapshot of the port-group CRDs (UDN/CUDN/NAD) and the physical fabric (nmstate
// NNS/NNCP), following the clusterstate/argo/desched reflector model - Catalog() is a
// pure in-memory scan, never a per-request cluster call.
//
// Port-group moves publish NetworkChanged so the hub re-broadcasts and the client
// re-pulls the out-of-band catalog; NNS (node-state) signals once at initial sync,
// then stays silent - its status churns and adapters need no live repaint, but a
// client that pulled before the sync must still learn the adapters exist. Each
// reflector is discovery-gated (like desched): a cluster without OVN-K UDN or nmstate
// simply serves an empty slice for that source, never an error loop. Node names come
// from a background-refreshed cache (nodes:list, no watch) that signals on membership
// change, so uplink membership stays off the request path without a nodes:watch grant.
package netstate

import (
	"context"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/reflect"
)

// gvr is the kind table's coordinates for one watched kind.
func gvr(kind string) schema.GroupVersionResource { return cluster.GVR(model.MustKind(kind)) }

var (
	gvrUDN  = gvr("UserDefinedNetwork")
	gvrCUDN = gvr("ClusterUserDefinedNetwork")
	gvrNAD  = gvr("NetworkAttachmentDefinition")
	gvrNNCP = gvr("NodeNetworkConfigurationPolicy")
	// Node state is read, never declared, so the table does not list it; it
	// shares nmstate's group with the policy.
	gvrNNS = schema.GroupVersionResource{Group: gvrNNCP.Group, Version: "v1beta1", Resource: "nodenetworkstates"}

	// The policy plane (see policies.go): the DFW tiers and the Tier-0/Tier-1
	// firewall + routing objects the Security view reads.
	gvrNetpol   = gvr("NetworkPolicy")
	gvrANP      = gvr("AdminNetworkPolicy")
	gvrBANP     = gvr("BaselineAdminNetworkPolicy")
	gvrEgressFW = gvr("EgressFirewall")
	gvrEgressIP = gvr("EgressIP")
	gvrExtRoute = gvr("AdminPolicyBasedExternalRoute")
)

// Snapshot holds the watch-fed networking stores. Build with New, start with Run;
// Catalog is safe for concurrent callers.
type Snapshot struct {
	sa  *cluster.Client
	bus *eventbus.Bus

	udn, cudn, nad, nns, nncp cache.Indexer
	nmstatePresent            atomic.Bool // the NNS CRD is served (nmstate installed)

	// Policy-plane stores; each empty until its CRD/API is discovered.
	netpol, anp, banp, egressfw, egressip, extroute cache.Indexer

	// watches pairs each store with its GVR, built once in New so Run and the
	// health flags cannot drift out of step when a kind is added.
	watches []watchSpec
	healthy []atomic.Bool // one reflect.TrackHealth flag per watches entry

	nodesMu sync.RWMutex
	nodes   []cluster.NodeLabels
}

// New builds the snapshot over sa (dotvirt's ServiceAccount client). bus may be nil in
// tests (Catalog reads the stores directly; only the change signal is suppressed).
func New(sa *cluster.Client, bus *eventbus.Bus) *Snapshot {
	idx := reflect.NewIndexer
	s := &Snapshot{
		sa: sa, bus: bus,
		udn: idx(), cudn: idx(), nad: idx(), nns: idx(), nncp: idx(),
		netpol: idx(), anp: idx(), banp: idx(), egressfw: idx(), egressip: idx(), extroute: idx(),
	}
	s.watches = []watchSpec{
		{gvrUDN, s.udn, true},
		{gvrCUDN, s.cudn, true},
		{gvrNAD, s.nad, true},
		{gvrNNCP, s.nncp, true},
		{gvrNNS, s.nns, false}, // NNS churn must not repaint the catalog
		{gvrNetpol, s.netpol, true},
		{gvrANP, s.anp, true},
		{gvrBANP, s.banp, true},
		{gvrEgressFW, s.egressfw, true},
		{gvrEgressIP, s.egressip, true},
		{gvrExtRoute, s.extroute, true},
	}
	s.healthy = make([]atomic.Bool, len(s.watches))
	for i := range s.healthy {
		s.healthy[i].Store(true) // a CRD never discovered must not read as stale
	}
	return s
}

// watchSpec is one reflector's wiring: its GVR, target store, and whether store
// moves signal NetworkChanged.
type watchSpec struct {
	gvr    schema.GroupVersionResource
	idx    cache.Indexer
	signal bool
}

// Healthy reports whether every started networking watch is currently established.
// The catalog keeps serving its last-good stores while unhealthy; the inventory
// surfaces a "may be stale" warning so a sustained outage isn't silent.
func (s *Snapshot) Healthy() bool { return reflect.AllHealthy(s.healthy) }

// nodeRefreshInterval bounds how stale uplink node membership can get; node add/remove
// is rare and this cache is off the request path, so a coarse poll is enough.
const nodeRefreshInterval = 2 * time.Minute

// Run starts one discovery-gated reflector per CRD plus the node-cache refresher, and
// returns immediately; everything stops when ctx is cancelled. Port-group and policy
// kinds signal NetworkChanged; NNS is watched silently.
func (s *Snapshot) Run(ctx context.Context) {
	for i := range s.watches {
		s.watch(ctx, s.watches[i], &s.healthy[i])
	}
	go s.refreshNodes(ctx)
}

// watch starts one spec's discovery-gated reflector. Serving the NNS CRD flips
// nmstatePresent, which gates the fabric UI.
func (s *Snapshot) watch(ctx context.Context, w watchSpec, healthy *atomic.Bool) {
	served := s.sa.HasAPIResource
	if w.gvr == gvrNNS {
		served = reflect.Served(served, &s.nmstatePresent)
	}
	onChange := func() {}
	var onSynced func()
	if s.bus != nil {
		if w.signal {
			onChange = func() { s.bus.Publish(eventbus.NetworkChanged) }
		} else {
			// A silent watch still signals its initial sync: a client that
			// pulled the catalog before this store filled would otherwise
			// never learn the data exists (nothing else bumps the version).
			onSynced = func() { s.bus.Publish(eventbus.NetworkChanged) }
		}
	}
	store := reflect.NewStore(w.idx, onChange, onSynced)
	reflect.RunWhenServed(ctx, served, w.gvr, s.sa.DynamicListWatch(w.gvr), store, healthy)
}

// refreshNodes keeps the node-name cache current via a periodic LIST (populated at once
// on start, then every nodeRefreshInterval). A changed node set signals NetworkChanged:
// uplink membership derives from it, and the first LIST usually lands after clients
// already pulled the catalog.
func (s *Snapshot) refreshNodes(ctx context.Context) {
	t := time.NewTicker(nodeRefreshInterval)
	defer t.Stop()
	for {
		if infos, err := s.sa.ListNodeLabels(ctx); err == nil {
			s.nodesMu.Lock()
			changed := !nodesEqual(s.nodes, infos)
			s.nodes = infos
			s.nodesMu.Unlock()
			if changed && s.bus != nil {
				s.bus.Publish(eventbus.NetworkChanged)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func nodesEqual(a, b []cluster.NodeLabels) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || !maps.Equal(a[i].Labels, b[i].Labels) {
			return false
		}
	}
	return true
}
