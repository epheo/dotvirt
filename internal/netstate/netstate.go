// Package netstate is dotvirt's networking read plane: an SA-maintained, watch-fed
// snapshot of the network CRDs (UDN/CUDN/NAD) and the physical fabric (nmstate
// NNS/NNCP), following the clusterstate/argo/drs reflector model - Catalog() is a
// pure in-memory read, never a per-request cluster call. Each read path
// decodes the stores it needs once per store generation, shared by every
// caller; only redaction runs per caller.
//
// Network moves publish NetworkChanged so the hub re-broadcasts and the client
// re-pulls the out-of-band catalog; NNS (node-state) signals once at initial sync,
// then stays silent - its status churns and adapters need no live repaint, but a
// client that pulled before the sync must still learn the adapters exist. Each
// reflector is discovery-gated (like drs): a cluster without OVN-K UDN or nmstate
// simply serves an empty slice for that source, never an error loop. Node names come
// from a background-refreshed cache (nodes:list, no watch) that signals on membership
// change, so uplink membership stays off the request path without a nodes:watch grant.
package netstate

import (
	"context"
	"maps"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/reflect"
)

var (
	gvrUDN  = cluster.GVR(model.KindUDN)
	gvrCUDN = cluster.GVR(model.KindCUDN)
	gvrNAD  = cluster.GVR(model.KindNAD)
	gvrNNCP = cluster.GVR(model.KindNNCP)
	// Node state is read, never declared, so the table does not list it; it
	// shares nmstate's group with the policy.
	gvrNNS = schema.GroupVersionResource{Group: gvrNNCP.Group, Version: "v1beta1", Resource: "nodenetworkstates"}
	// One enactment per (node, policy): where an uplink policy failed to apply.
	gvrNNCE = schema.GroupVersionResource{Group: gvrNNCP.Group, Version: "v1beta1", Resource: "nodenetworkconfigurationenactments"}

	// The policy plane (see policies.go): the rule tiers, outbound rules and
	// cluster edge objects the Security view reads.
	gvrNetpol   = cluster.GVR(model.KindNetpol)
	gvrANP      = cluster.GVR(model.KindANP)
	gvrBANP     = cluster.GVR(model.KindBANP)
	gvrEgressFW = cluster.GVR(model.KindEgressFW)
	gvrEgressIP = cluster.GVR(model.KindEgressIP)
	gvrExtRoute = cluster.GVR(model.KindExtRoute)

	// Only discovered, never listed: whether the cluster can advertise pod
	// networks, which may join networks OVN-K otherwise isolates.
	gvrRouteAds = schema.GroupVersionResource{Group: gvrUDN.Group, Version: "v1", Resource: "routeadvertisements"}
)

// What discovery last said of the RouteAdvertisements API.
const (
	routeAdsUnknown int32 = iota // no probe has answered yet
	routeAdsAbsent
	routeAdsServed
)

// Snapshot holds the watch-fed networking stores. Build with New, start with Run;
// Catalog is safe for concurrent callers.
type Snapshot struct {
	sa  *cluster.Client
	bus *eventbus.Bus

	udn, cudn, nad, nns, nncp, nnce cache.Indexer
	nmstatePresent                  atomic.Bool // the NNS CRD is served (nmstate installed)
	// The same stores decoded once per generation.
	nets        memo[[3]uint64, *netView]
	uplinkMemo  memo[[3]uint64, []model.Uplink]
	adapterMemo memo[uint64, []model.PhysicalAdapter]

	// Policy-plane stores; each empty until its CRD/API is discovered.
	netpol, anp, banp, egressfw, egressip, extroute cache.Indexer
	// The same stores decoded once per generation, and the Policies rows
	// built from those planes.
	netpols, anps, banps, firewalls, egressIPs, extRoutes *policyStore
	policyRows                                            memo[[6]*plane, []model.Policy]

	// watches pairs each store with its GVR, built once in New so Run and the
	// health flags cannot drift out of step when a kind is added.
	watches []watchSpec
	healthy []atomic.Bool // one reflect.TrackHealth flag per watches entry

	// The cluster's infrastructure ranges, refreshed off the request path; nil
	// is unknown.
	nodesMu    sync.RWMutex
	nodes      []cluster.NodeLabels
	nodesGen   uint64         // moves when nodes does
	podNet     []netip.Prefix // the default network's node subnets
	clusterNet []netip.Prefix // the pod network CIDRs the cluster config declares
	svcNet     []netip.Prefix // the service network CIDRs

	routeAds atomic.Int32 // routeAdsUnknown, routeAdsAbsent or routeAdsServed
}

// New builds the snapshot over sa (dotvirt's ServiceAccount client). bus may be nil in
// tests (Catalog reads the stores directly; only the change signal is suppressed).
func New(sa *cluster.Client, bus *eventbus.Bus) *Snapshot {
	idx := reflect.NewIndexer
	s := &Snapshot{
		sa: sa, bus: bus,
		udn: idx(), cudn: idx(), nad: idx(), nns: idx(), nncp: idx(), nnce: idx(),
		netpol: idx(), anp: idx(), banp: idx(), egressfw: idx(), egressip: idx(), extroute: idx(),
	}
	for _, idx := range []cache.Indexer{s.udn, s.cudn, s.nad, s.nns, s.nncp, s.nnce} {
		mustCount(idx) // the network view, uplinks and adapters memoize on them
	}
	s.netpols = newPolicyStore(s.netpol, decodeNetpol, nil)
	s.anps = newPolicyStore(s.anp, decodeANP, byPrecedence)
	s.banps = newPolicyStore(s.banp, decodeBANP, nil)
	s.firewalls = newPolicyStore(s.egressfw, decodeGateway, nil)
	s.egressIPs = newPolicyStore(s.egressip, decodeEgressIP, nil)
	s.extRoutes = newPolicyStore(s.extroute, decodeExtRoute, nil)
	s.watches = []watchSpec{
		{gvrUDN, s.udn, true, false},
		{gvrCUDN, s.cudn, true, false},
		{gvrNAD, s.nad, true, false},
		{gvrNNCP, s.nncp, true, false},
		{gvrNNS, s.nns, false, false}, // NNS churn must not repaint the catalog
		{gvrNNCE, s.nnce, true, true},
		{gvrNetpol, s.netpol, true, false},
		{gvrANP, s.anp, true, false},
		{gvrBANP, s.banp, true, false},
		{gvrEgressFW, s.egressfw, true, false},
		{gvrEgressIP, s.egressip, true, false},
		{gvrExtRoute, s.extroute, true, false},
	}
	s.healthy = make([]atomic.Bool, len(s.watches))
	for i := range s.healthy {
		s.healthy[i].Store(true) // a CRD never discovered must not read as stale
	}
	return s
}

// watchSpec is one reflector's wiring: its GVR, target store, whether store
// moves signal NetworkChanged, and whether the watch is optional.
type watchSpec struct {
	gvr    schema.GroupVersionResource
	idx    cache.Indexer
	signal bool
	// optional: the data only enriches the catalog (per-host uplink failures),
	// and an operator release may not grant it yet. The watch starts only once
	// the SA may list the kind, and its failures never read as staleness.
	optional bool
}

// listable gates an optional watch: the kind is served and the SA may list it.
// A review error reads as "not yet"; the gate re-probes on the discovery pace.
func (s *Snapshot) listable(ctx context.Context) func(schema.GroupVersionResource) bool {
	return func(gvr schema.GroupVersionResource) bool {
		if !s.sa.HasAPIResource(gvr) {
			return false
		}
		ok, err := s.sa.Can(ctx, "list", gvr.Group, gvr.Resource, "")
		return err == nil && ok
	}
}

// Holds reports whether a store holds the object of resource (group, plural)
// named namespace/name ("" for a cluster-scoped one). A kind no store
// watches reads as absent.
func (s *Snapshot) Holds(group, resource, namespace, name string) bool {
	key := name
	if namespace != "" {
		key = namespace + "/" + name
	}
	for _, w := range s.watches {
		if w.gvr.Group == group && w.gvr.Resource == resource {
			_, ok, _ := w.idx.GetByKey(key)
			return ok
		}
	}
	return false
}

// Healthy reports whether every started networking watch is currently established.
// The catalog keeps serving its last-good stores while unhealthy; the inventory
// surfaces a "may be stale" warning so a sustained outage isn't silent.
func (s *Snapshot) Healthy() bool { return reflect.AllHealthy(s.healthy) }

// nodeRefreshInterval bounds how stale uplink node membership can get; node add/remove
// is rare and this cache is off the request path, so a coarse poll is enough.
const nodeRefreshInterval = 2 * time.Minute

// Run starts one discovery-gated reflector per CRD plus the node-cache refresher, and
// returns immediately; everything stops when ctx is cancelled. Network and policy
// kinds signal NetworkChanged; NNS is watched silently.
func (s *Snapshot) Run(ctx context.Context) {
	for i := range s.watches {
		s.watch(ctx, s.watches[i], &s.healthy[i])
	}
	go s.refreshNodes(ctx)
}

// ovnServed gates the k8s.ovn.org watches, and from the same discovery read
// records whether the RouteAdvertisements API is served. A failed read keeps
// the last answer.
func (s *Snapshot) ovnServed(gvr schema.GroupVersionResource) bool {
	served, err := s.sa.ServedResources(gvr.GroupVersion())
	if err != nil {
		return false
	}
	s.recordRouteAds(served)
	return served[gvr.Resource]
}

// probeRouteAds re-reads discovery on the node tick until RouteAdvertisements
// is served: once every OVN-K watch is admitted no gate reads it, and it may
// be installed later.
func (s *Snapshot) probeRouteAds() {
	if s.routeAds.Load() == routeAdsServed {
		return
	}
	if served, err := s.sa.ServedResources(gvrRouteAds.GroupVersion()); err == nil {
		s.recordRouteAds(served)
	}
}

func (s *Snapshot) recordRouteAds(served map[string]bool) {
	if served[gvrRouteAds.Resource] {
		s.routeAds.Store(routeAdsServed)
	} else {
		s.routeAds.Store(routeAdsAbsent)
	}
}

// mayJoin reports whether route advertisement may join primary networks
// OVN-K otherwise isolates: its API is served, or discovery has not said.
func (s *Snapshot) mayJoin() bool { return s.routeAds.Load() != routeAdsAbsent }

// watch starts one spec's discovery-gated reflector. Serving the NNS CRD flips
// nmstatePresent, which gates the fabric UI.
func (s *Snapshot) watch(ctx context.Context, w watchSpec, healthy *atomic.Bool) {
	served := s.sa.HasAPIResource
	if w.gvr.GroupVersion() == gvrRouteAds.GroupVersion() {
		served = s.ovnServed
	}
	if w.gvr == gvrNNS {
		served = reflect.Served(served, &s.nmstatePresent)
	}
	if w.optional {
		served = s.listable(ctx)
		healthy = new(atomic.Bool)
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
	// Every watch, silent ones included, publishes its health flips: the frame's
	// stale-catalog warning reads them.
	flip := func() { s.bus.Publish(eventbus.NetworkChanged) }
	reflect.RunWhenServed(ctx, served, w.gvr, s.sa.DynamicListWatch(w.gvr), store, healthy, flip)
}

// refreshNodes keeps the node-name cache current via a periodic LIST (populated at once
// on start, then every nodeRefreshInterval). A changed node set signals NetworkChanged:
// uplink membership derives from it, and the first LIST usually lands after clients
// already pulled the catalog. The same LIST carries the default network's node
// subnets, and the same tick reads the cluster's pod and service networks
// and, until it is served, whether route advertisement is: only the trace
// reads those, so their moves signal nothing.
func (s *Snapshot) refreshNodes(ctx context.Context) {
	t := time.NewTicker(nodeRefreshInterval)
	defer t.Stop()
	for {
		if infos, err := s.sa.ListNodeLabels(ctx); err == nil && s.setNodes(infos) && s.bus != nil {
			s.bus.Publish(eventbus.NetworkChanged)
		}
		s.refreshClusterNetworks(ctx)
		s.probeRouteAds()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refreshClusterNetworks reads the cluster's declared pod and service
// networks. A cluster without OpenShift's Network config, or one the SA may
// not read, leaves them unknown; a failed read keeps the last one.
func (s *Snapshot) refreshClusterNetworks(ctx context.Context) {
	pod, svc, err := s.sa.ClusterNetworks(ctx)
	switch {
	case apierrors.IsNotFound(err), apierrors.IsForbidden(err):
		pod, svc = nil, nil
	case err != nil:
		return
	}
	clusterNet, svcNet := parsePrefixes(pod), parsePrefixes(svc)
	s.nodesMu.Lock()
	s.clusterNet, s.svcNet = clusterNet, svcNet
	s.nodesMu.Unlock()
}

// setNodes records a node LIST and its default network subnets, and reports
// whether the node set moved.
func (s *Snapshot) setNodes(infos []cluster.NodeLabels) bool {
	podNet := defaultPodSubnets(infos)
	s.nodesMu.Lock()
	defer s.nodesMu.Unlock()
	changed := !nodesEqual(s.nodes, infos)
	s.nodes, s.podNet = infos, podNet
	if changed {
		s.nodesGen++
	}
	return changed
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
