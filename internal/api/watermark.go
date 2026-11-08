package api

import (
	"context"
	"encoding/binary"
	"hash/maphash"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
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
// visible set: node read (the fabric, uplinks and node ops), platform
// authoring, and the create authorities behind CanManage, Caps, the
// cluster-tier policy rows, shared-error redaction and platform source files.
// Each is the cached review the pulls themselves make, so a grant or a
// revocation that leaves the visible set alone still moves the watermark.
type netGrants struct {
	nodes  bool
	create map[ssarRef]bool
	bits   uint64 // every verdict, platform authoring included, in a fixed order
}

// netGrantRefs are the create authorities netGrants reads; the Caps ones
// only matter where a platform repo is configured.
var (
	netGrantRefs  = []ssarRef{ssarCUDN, ssarANP, ssarBANP, ssarEgressIP, ssarExtRoute}
	netCapRefs    = []ssarRef{ssarUplink, ssarNamespace}
	netGrantKinds = []model.Kind{model.KindCUDN, model.KindNNCP, model.KindANP, model.KindBANP, model.KindEgressIP, model.KindExtRoute}
)

func (s *Server) netGrants(ctx context.Context, id auth.Identity, c *cluster.Client, platform bool) netGrants {
	g := netGrants{create: map[ssarRef]bool{}}
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
	for i, v := range verdicts {
		if v {
			g.bits |= 1 << i
		}
	}
	return g
}

// sees admits the cluster-scoped kinds the caller's pulls show: shared
// networks to everyone, each cluster-tier policy kind to its authority, and
// the fabric (uplink policies, any other cluster-scoped kind) to node readers.
func (g netGrants) sees(gvr schema.GroupVersionResource) bool {
	switch gvr {
	case cluster.GVR(model.KindCUDN):
		return true
	case cluster.GVR(model.KindANP), cluster.GVR(model.KindBANP), cluster.GVR(model.KindEgressIP), cluster.GVR(model.KindExtRoute):
		return g.create[ssarRef{gvr.Group, gvr.Resource}]
	}
	return g.nodes
}

// networksWatermark covers what the caller's networks and policies pulls
// read: the catalog objects in its visible namespaces and the cluster-scoped
// kinds it is shown (netstate.Watermark), their drift by the same scopes, the
// visible set itself, which filters both pulls, and the verdicts that shape
// them.
func (s *Server) networksWatermark(visible map[string]bool, g netGrants) uint64 {
	var sum uint64
	var t term
	if s.netstate != nil {
		sum = s.netstate.Watermark(visible, g.sees, g.nodes)
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

// tasksWatermark covers the entries the caller's tasks pull returns (taskScope
// over the same projects and node signal), each with the project it shows
// under.
func (s *Server) tasksWatermark(projects []project.ProjectInfo, canNodes bool) uint64 {
	if s.tasks == nil {
		return 0
	}
	sc := newTaskScope(projects, canNodes)
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
