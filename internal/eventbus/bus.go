// Package eventbus is dotvirt's change-propagation primitive: a typed pub/sub that
// carries both an EDGE (a coalesced wake when a kind moves) and a LEVEL (a monotonic
// per-kind version). Consumers reconcile to the level — they wake on the edge, read
// the current Version of the kinds they depend on, do their work, and re-check the
// Version; if it hasn't moved they're caught up. This is what lets every rebuild
// path stay current without a debounce (coalescing falls out of the work duration)
// or a TTL (invalidation falls out of comparing a version).
//
// A single channel can only notify ONE consumer; the fan-out is what lets the
// exporter, the proposals refresher, and the inventory hub ride the same events.
// Typed kinds keep a consumer from waking on churn it doesn't care about — the
// exporter depends on VMSpecChanged + NamespaceChanged, so a VMI status heartbeat
// (LiveChanged) never wakes it.
package eventbus

import (
	"sync"
	"sync/atomic"
)

// Kind identifies what moved. Kinds are deliberately fine-grained so a consumer
// depends on exactly the changes that affect its output.
type Kind int

const (
	// VMSpecChanged: a VirtualMachine's spec/generation changed, or a VM was added or
	// removed — the exported manifest set moved. (NOT fired on VM status churn.)
	VMSpecChanged Kind = iota
	// LiveChanged: runtime state moved — a VMI status change, or a VM status/existence
	// change. The live inventory tree's per-VM phase/IP/node.
	LiveChanged
	// NamespaceChanged: a project namespace was added, removed, or relabeled — both an
	// inventory topology change AND a visibility (RBAC) change.
	NamespaceChanged
	// RBACChanged: a RoleBinding moved, so a token's visible-namespace set may have
	// changed — the cue to revalidate the per-token visibility cache.
	RBACChanged
	// DriftChanged: an ArgoCD Application moved — sync/health drift.
	DriftChanged
	// GitChanged: a project repo's branch heads moved (a push/merge, seen via the
	// forge webhook or the backstop poll).
	GitChanged
	// ProposalsChanged: a token's open-PR lane changed (the refresher found a diff).
	ProposalsChanged
	// NetworkChanged: a port-group CRD (UDN/CUDN/NAD/NNCP) moved — the network catalog,
	// fetched out-of-band from the inventory frame, needs re-pulling.
	NetworkChanged

	// kindCount is the number of kinds — keep it last. Sizes the version array.
	kindCount
)

// Bus fans typed change signals out to subscribers and tracks a monotonic version
// per kind. One Bus per process. The zero value is not usable; build one with New.
// Safe for concurrent Publish/Subscribe/Version.
type Bus struct {
	// versions[k] increments on each Publish(k). Read via Version (lock-free atomics);
	// it is the LEVEL consumers reconcile against. Monotonic PER KIND — use it for
	// equality only (t := Version(); work(); Version() == t), never to diff magnitudes
	// (the sum of independent atomics is not a serializable global clock).
	versions [kindCount]atomic.Uint64

	mu   sync.RWMutex
	subs map[*subscription]struct{}
}

type subscription struct {
	kinds map[Kind]struct{}
	ch    chan struct{}
}

// New builds an empty Bus.
func New() *Bus {
	return &Bus{subs: map[*subscription]struct{}{}}
}

// Publish bumps k's version, then wakes every subscriber registered for k. The bump
// happens BEFORE the wake so a consumer woken by this call is guaranteed to observe
// the incremented version when it calls Version (no lost-wakeup staleness). The send
// is non-blocking and coalesced: each subscriber's channel is 1-buffered, so a burst
// collapses into one pending wake. Nil-safe (a nil Bus is a no-op).
func (b *Bus) Publish(k Kind) {
	if b == nil {
		return
	}
	b.versions[k].Add(1)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if _, ok := s.kinds[k]; !ok {
			continue
		}
		select {
		case s.ch <- struct{}{}:
		default: // a wake is already pending for this subscriber
		}
	}
}

// Version returns the summed version of the named kinds — the watermark a consumer
// reconciles against. Strictly increases whenever ANY of those kinds is published,
// so `t := Version(ks...); work(); Version(ks...) == t` reliably means "nothing I
// depend on moved while I worked." Lock-free. Nil-safe (returns 0). Equality-only:
// do not subtract two readings.
func (b *Bus) Version(kinds ...Kind) uint64 {
	if b == nil {
		return 0
	}
	var v uint64
	for _, k := range kinds {
		v += b.versions[k].Load()
	}
	return v
}

// Subscribe registers interest in the given kinds and returns a 1-buffered wake
// channel plus a cancel func that unsubscribes (and is safe to call more than
// once). Subscribing to no kinds yields a channel that never fires. The channel is
// never closed by the bus — consumers select on it alongside their ctx.Done. A nil
// Bus yields a nil channel (which never fires) and a no-op cancel, so a consumer
// with signalling disabled (e.g. in tests) falls back to its own backstop tick.
func (b *Bus) Subscribe(kinds ...Kind) (<-chan struct{}, func()) {
	if b == nil {
		return nil, func() {}
	}
	s := &subscription{kinds: make(map[Kind]struct{}, len(kinds)), ch: make(chan struct{}, 1)}
	for _, k := range kinds {
		s.kinds[k] = struct{}{}
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, s)
			b.mu.Unlock()
		})
	}
}
