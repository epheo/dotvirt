package api

import (
	"context"
	"encoding/binary"
	"hash/maphash"
	"maps"
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/tasks"
)

// The frame's out-of-band watermarks. NetworksVersion and TasksVersion tell a
// client when to re-pull /api/networks with /api/policies, and /api/tasks.
// Each fingerprints exactly what that caller's pull reads, so it moves with
// the caller's own changes and the shared catalog, never with another
// tenant's act: a global counter would show every tenant when the others act,
// and resend every frame on each act. Equality-only, like the bus versions.

// wireMask keeps a watermark exact as a JavaScript number.
const wireMask = 1<<53 - 1

// watermarkSeed keys watermarks, which are compared within one process only:
// a restart moves every caller's, so each re-pulls once.
var watermarkSeed = maphash.MakeSeed()

// term hashes one fingerprint term. Callers sum terms, so order never
// matters; start resets it for the next.
type term struct{ h maphash.Hash }

func (t *term) start(kind string) *term {
	t.h.SetSeed(watermarkSeed)
	return t.str(kind)
}

func (t *term) str(ss ...string) *term {
	for _, s := range ss {
		_, _ = t.h.WriteString(s)
		_ = t.h.WriteByte(0)
	}
	return t
}

func (t *term) num(n uint64) *term {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], n)
	_, _ = t.h.Write(b[:])
	return t
}

func (t *term) sum() uint64 { return t.h.Sum64() }

// netGrants are the verdicts the network and policy pulls apply beyond the
// view: node read (the fabric, uplinks and node ops), platform
// authoring, the create authorities behind shared-error redaction and
// platform source files, and list on each cluster-scoped kind the pulls show,
// which also names a cluster rule on every caller's rules pull. Each is the cached review the
// pulls themselves make, so a grant or a revocation that leaves the visible
// set alone still moves the watermark.
type netGrants struct {
	nodes  bool
	create map[ssarRef]bool
	list   map[ssarRef]bool
	bits   uint64 // every verdict, platform authoring included, in a fixed order
}

// netGrantRefs are the create authorities netGrants reads, netListRefs the
// kinds it asks list on; netCapRefs only matter where a platform repo is
// configured: an uplink's source file and drift.
var (
	netGrantRefs  = []ssarRef{ssarCUDN}
	netListRefs   = []ssarRef{ssarCUDN, ssarUplink, ssarNNS, ssarANP, ssarBANP, ssarEgressIP, ssarExtRoute}
	netCapRefs    = []ssarRef{ssarUplink}
	netGrantKinds = []model.Kind{model.KindCUDN, model.KindNNCP, model.KindANP, model.KindBANP, model.KindEgressIP, model.KindExtRoute}
)

func (s *Server) netGrants(ctx context.Context, id auth.Identity, c *cluster.Client, platform bool) netGrants {
	g := netGrants{create: map[ssarRef]bool{}, list: map[ssarRef]bool{}}
	g.nodes, _ = s.can(ctx, id, c, refQ(verbList, ssarNodes, ""))
	verdicts := []bool{g.nodes, platform}
	refs := netGrantRefs
	if s.cfg.PlatformRepo != "" {
		refs = append(refs[:len(refs):len(refs)], netCapRefs...)
	}
	for _, r := range refs {
		g.create[r], _ = s.can(ctx, id, c, refQ(verbCreate, r, ""))
		verdicts = append(verdicts, g.create[r])
	}
	for _, r := range netListRefs {
		g.list[r], _ = s.can(ctx, id, c, refQ(verbList, r, ""))
		verdicts = append(verdicts, g.list[r])
	}
	for i, v := range verdicts {
		if v {
			g.bits |= 1 << i
		}
	}
	return g
}

// sees admits the cluster-scoped kinds the caller's pulls show, each by its own
// list right. A tenant's
// shared network moves its NAD, which the namespaced half of the watermark sees.
func (g netGrants) sees(gvr schema.GroupVersionResource) bool {
	switch gvr {
	case cluster.GVR(model.KindCUDN):
		return g.list[ssarCUDN]
	case cluster.GVR(model.KindANP), cluster.GVR(model.KindBANP), cluster.GVR(model.KindEgressIP), cluster.GVR(model.KindExtRoute):
		return g.list[ssarRef{gvr.Group, gvr.Resource}]
	}
	return g.list[ssarUplink]
}

// fabric reports whether the caller is shown any of the node-level fabric.
func (g netGrants) fabric() bool { return g.list[ssarUplink] || g.list[ssarNNS] }

// networksWatermark covers what the caller's networks and policies pulls
// read: the catalog objects in its visible namespaces and the cluster-scoped
// kinds it is shown (netstate.Watermark), their drift by the same scopes, the
// view itself, which filters both pulls, and the verdicts that shape
// them.
func (s *Server) networksWatermark(v callerView, g netGrants) uint64 {
	visible := v.names
	var sum uint64
	var t term
	if s.netstate != nil {
		lists := func(gvr schema.GroupVersionResource, ns string) bool {
			return v.lists(ssarRef{gvr.Group, gvr.Resource}, ns)
		}
		sum = s.netstate.Watermark(visible, g.sees, lists, g.fabric())
	}
	drift := func(scope string) uint64 {
		if s.drift == nil {
			return 0
		}
		return s.drift.ObjectDriftGen(scope)
	}
	for _, k := range netGrantKinds {
		if g.sees(cluster.GVR(k)) {
			scope := argo.ClusterScope(k.Group, k.Kind)
			sum += t.start("drift").str(scope).num(drift(scope)).sum()
		}
	}
	for ns := range visible {
		sum += t.start("namespace").str(ns).num(drift(ns)).sum()
	}
	sum += t.start("grants").num(g.bits).sum()
	return sum & wireMask
}

// replicationWatermark covers what the caller's replication pulls read: every
// disk for an operator, the disks of the namespaces whose claims it may list
// for anyone else. The reviews are the cached ones the pull itself makes, so a
// grant moves it.
func (s *Server) replicationWatermark(ctx context.Context, id auth.Identity, c *cluster.Client, v callerView) uint64 {
	if s.replication == nil || !s.replication.Started() {
		return 0
	}
	var t term
	if s.capable(ctx, id, c, replicationRead) {
		return t.start("replication-operator").num(s.replication.Total()).sum() & wireMask
	}
	var sum uint64
	for _, ns := range v.listsIn(ssarClaims, slices.Collect(maps.Keys(v.names))) {
		sum += t.start("replication").str(ns).num(s.replication.Gen(ns)).sum()
	}
	return sum & wireMask
}

// routesWatermark covers what the caller's Gateway and route pulls read: every
// route for an operator, the kinds it may list in its visible namespaces for
// anyone else.
func (s *Server) routesWatermark(ctx context.Context, id auth.Identity, c *cluster.Client, visible map[string]bool) uint64 {
	if s.frontDoor == nil || !s.frontDoor.Started() {
		return 0
	}
	var t term
	if s.capable(ctx, id, c, gatewaysRead) {
		return t.start("routes-operator").num(s.frontDoor.Total()).sum() & wireMask
	}
	var sum uint64
	for h := range s.routesReadable(ctx, id, c, slices.Collect(maps.Keys(visible))) {
		sum += t.start("routes").str(h.Namespace, h.Resource).num(s.frontDoor.Gen(h.Namespace, h.Resource)).sum()
	}
	return sum & wireMask
}

// tasksWatermark covers the entries the caller's tasks pull returns (the
// same taskScope), each with the project it shows under.
func (s *Server) tasksWatermark(sc taskScope) uint64 {
	if s.tasks == nil {
		return 0
	}
	var sum uint64
	var t term
	s.tasks.Visit(func(o tasks.Op) {
		if proj, ok := sc.op(o); ok {
			sum += t.start("op").str(proj, o.Verb, o.Namespace, o.Name, o.By).num(okBit(o.OK)).num(uint64(o.At.UnixNano())).sum()
		}
	}, func(m tasks.Merge) {
		if proj, ok := sc.merge(m); ok {
			sum += t.start("merge").str(proj, m.RepoURL, m.URL, m.Title, m.By).num(uint64(m.Number)).num(uint64(m.At.UnixNano())).sum()
		}
	})
	return sum & wireMask
}

func okBit(ok bool) uint64 {
	if ok {
		return 1
	}
	return 0
}
