package api

import (
	"encoding/binary"
	"hash/maphash"

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

// networksWatermark covers what the caller's networks and policies pulls
// read: the catalog objects in its visible namespaces and the cluster-scoped
// ones (netstate.Watermark), their drift by the same scopes, and the visible
// set itself, which filters both pulls.
func (s *Server) networksWatermark(visible map[string]bool, canNodes bool) uint64 {
	var sum uint64
	var t term
	if s.netstate != nil {
		sum = s.netstate.Watermark(visible, canNodes)
	}
	drift := func(scope string) uint64 {
		if s.drift == nil {
			return 0
		}
		return s.drift.ObjectDriftGen(scope)
	}
	sum += t.start("drift").num(drift("")).sum()
	for ns := range visible {
		sum += t.start("namespace").str(ns).num(drift(ns)).sum()
	}
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
