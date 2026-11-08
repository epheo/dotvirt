// Package eventbus is dotvirt's change-propagation primitive: a typed pub/sub that
// carries both an EDGE (a coalesced wake when a kind moves) and a LEVEL (a monotonic
// per-kind version). Consumers reconcile to the level - they wake on the edge, read
// the current Version of the kinds they depend on, do their work, and re-check the
// Version; if it hasn't moved they're caught up. This is what lets every rebuild
// path stay current without a debounce (coalescing falls out of the work duration)
// or a TTL (invalidation falls out of comparing a version).
//
// A single channel can only notify ONE consumer; the fan-out is what lets the
// inventory hub and the proposals refresher ride the same events. Typed kinds
// keep a consumer from waking on churn it doesn't care about - the proposals
// refresher subscribes to GitChanged alone, so a VMI status heartbeat
// (LiveChanged) never re-queries the forge.
//
// A kind can also be published per key (PublishKey): GitChanged is keyed by repo,
// so a consumer scoped to some repos reconciles against KeyVersion of those alone
// and a push to one tenant's repo leaves every other tenant's work untouched.
// RBACChanged is keyed by the namespace whose grants moved, so a review in one
// namespace survives a move in another (OtherRBACChanged outside projects).
package eventbus

import (
	"sync"
	"sync/atomic"
)

// Kind identifies what moved. Kinds are deliberately fine-grained so a consumer
// depends on exactly the changes that affect its output.
type Kind int

const (
	// LiveChanged: a VM's live state moved - a VM or VMI added or removed, or a
	// change to what the inventory shows of a VMI (phase, IPs, node, guest facts,
	// migration) - or a cluster watch's health flipped. VM and VMI status writes
	// that move none of it are not published.
	LiveChanged Kind = iota
	// NamespaceChanged: a project namespace was added, removed, or relabeled - both an
	// inventory topology change AND a change of the namespaces a token's
	// visibility is reviewed over.
	NamespaceChanged
	// RBACChanged: a grant moved - a RoleBinding or a Role one binds, a
	// ClusterRoleBinding, a ClusterRole one of those binds, or an OpenShift
	// Group's members - so a token's visible namespaces and review verdicts may
	// have changed: the cue to revalidate the per-token authorization caches.
	// A RoleBinding or Role grants only in its namespace, so it publishes keyed
	// by that namespace while it is a project's; the cluster-scoped kinds
	// publish unkeyed.
	RBACChanged
	// OtherRBACChanged: a RoleBinding or Role moved in a namespace that is no
	// project's, keyed by that namespace, never unkeyed. A frame asks only of
	// project namespaces and cluster scope, so no frame depends on it and the
	// hub never wakes on it; a review in one of those namespaces reads both
	// kinds' marks, which also covers a namespace that became a project as
	// its grants moved.
	OtherRBACChanged
	// DriftChanged: the drift Argo reports moved - an object's sync/health/apply
	// error or a project's rollup - or the Applications watch's health flipped.
	// An Application write that moves none of it (each reconcile) is not published.
	DriftChanged
	// GitChanged: a project repo's branch heads moved (a push/merge, seen via the
	// forge webhook or the backstop poll). Published per repo: the key is the
	// canonical repo URL (forge.NormalizeRepoURL).
	GitChanged
	// ProposalsChanged: a token's open-PR lane changed (the refresher found a diff).
	ProposalsChanged
	// NetworkChanged: a port-group CRD (UDN/CUDN/NAD/NNCP) moved - the network catalog,
	// fetched out-of-band from the inventory frame, needs re-pulling.
	NetworkChanged
	// TaskChanged: the recent-tasks feed moved - an imperative op was recorded or a
	// merged PR landed. The feed is fetched out-of-band, like the network catalog.
	TaskChanged
	// TTLBackstop: TTL-cached authorization no RBAC watch observes (see
	// api.RunTTLBackstop) may have expired - published on a fixed cadence while a
	// stream is open so frames re-evaluate it. Keep it out of every cache's
	// version stamp: it must not invalidate anything before its TTL.
	TTLBackstop
	// SessionEnded: sessions ended - a logout, or a TokenReview rejected their
	// token - the cue to close their streams and consoles. No frame depends on
	// it: keep it out of every version stamp.
	SessionEnded

	// kindCount is the number of kinds - keep it last. Sizes the version array.
	kindCount
)

// Bus fans typed change signals out to subscribers and tracks a monotonic version
// per kind. One Bus per process. The zero value is not usable; build one with New.
// Safe for concurrent Publish/Subscribe/Version.
type Bus struct {
	// versions[k] increments on each Publish(k). Read via Version (lock-free atomics);
	// it is the LEVEL consumers reconcile against. Monotonic PER KIND - use it for
	// equality only (t := Version(); work(); Version() == t), never to diff magnitudes
	// (the sum of independent atomics is not a serializable global clock).
	versions [kindCount]atomic.Uint64

	// keys[k] is k's key levels. A publish moves versions[k] and them under
	// keys[k].mu, which every read of them takes: a reader that sees Version(k)
	// move, then reads a key level, sees the publish that moved it, so a stamp
	// of Version is never newer than the key levels a build reads after it.
	keys [kindCount]keyLevels

	mu   sync.RWMutex
	subs map[*subscription]struct{}
}

// keyLevels is one kind's key state. broad counts unkeyed publishes, part of
// every key's version, so an unkeyed publish moves every key. keyed counts
// each key's publishes; seq counts every keyed publish, and marks holds the
// seq each key's last one reached.
type keyLevels struct {
	mu    sync.Mutex
	broad uint64
	seq   uint64
	keyed map[string]uint64
	marks map[string]uint64
}

type subscription struct {
	kinds map[Kind]struct{}
	ch    chan struct{}
}

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
	l := &b.keys[k]
	l.mu.Lock()
	b.versions[k].Add(1)
	l.broad++
	l.mu.Unlock()
	b.wake(k)
}

// PublishKey is Publish scoped to one key of k: Version(k) and KeyVersion(k, key)
// move, other keys of k do not. The kind's version is bumped before the key's, so
// a reader that sees the key move also sees Version(k) move when it reads it next
// (Publish keeps the same order).
func (b *Bus) PublishKey(k Kind, key string) {
	if b == nil {
		return
	}
	l := &b.keys[k]
	l.mu.Lock()
	b.versions[k].Add(1)
	if l.keyed == nil {
		l.keyed, l.marks = map[string]uint64{}, map[string]uint64{}
	}
	l.keyed[key]++
	l.seq++
	l.marks[key] = l.seq
	l.mu.Unlock()
	b.wake(k)
}

// KeyVersion is the level of one key of k: it moves on PublishKey(k, key) and on
// every unkeyed Publish(k). Equality-only, like Version. Nil-safe (returns 0).
// It counts the key's publishes, so it restarts once the key is forgotten.
func (b *Bus) KeyVersion(k Kind, key string) uint64 {
	if b == nil {
		return 0
	}
	l := &b.keys[k]
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.keyed[key] + l.broad
}

// BroadVersion is the level of k's unkeyed publishes alone, which move every
// key of k: what a consumer depends on when no key of k concerns it.
// Equality-only. Nil-safe (returns 0).
func (b *Bus) BroadVersion(k Kind) uint64 {
	if b == nil {
		return 0
	}
	l := &b.keys[k]
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.broad
}

// KeySeq counts k's keyed publishes, each of which marks its key with the
// count it reached: a consumer that reads KeySeq, works, then asks MovedSince
// with that reading learns every key published after it, and none it already
// saw. Nil-safe (returns 0).
func (b *Bus) KeySeq(k Kind) uint64 {
	if b == nil {
		return 0
	}
	l := &b.keys[k]
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// KeyMark is the KeySeq count key's last publish reached, 0 when it has none
// or was forgotten. A later publish marks it above every mark it had, so a
// stamp of a mark never matches again once the key moves, forgotten between
// or not. Unkeyed publishes leave it alone (BroadVersion). Nil-safe.
func (b *Bus) KeyMark(k Kind, key string) uint64 {
	if b == nil {
		return 0
	}
	l := &b.keys[k]
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.marks[key]
}

// MovedSince reports which of keys k published after since, a KeySeq reading.
// Nil-safe (none).
func (b *Bus) MovedSince(k Kind, since uint64, keys []string) []string {
	if b == nil {
		return nil
	}
	l := &b.keys[k]
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, key := range keys {
		if l.marks[key] > since {
			out = append(out, key)
		}
	}
	return out
}

// Forget drops what k holds of key, so a kind keyed by something short-lived
// does not grow without bound. Until key is published again it reads as never
// published: KeyMark 0, KeyVersion restarted, absent from MovedSince. Forget a
// key only once no stamp of it read before its last publish can still serve,
// and only of a kind whose consumers stamp KeyMark, which never re-equals.
// Nil-safe.
func (b *Bus) Forget(k Kind, key string) {
	if b == nil {
		return
	}
	l := &b.keys[k]
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.keyed, key)
	delete(l.marks, key)
}

func (b *Bus) wake(k Kind) {
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

// Version returns the summed version of the named kinds - the watermark a consumer
// reconciles against. Strictly increases whenever ANY of those kinds is published,
// so `t := Version(ks...); work(); Version(ks...) == t` reliably means "nothing I
// depend on moved while I worked." Lock-free; a key level read after it is at
// least as new (see Bus.keys). Nil-safe (returns 0). Equality-only:
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
// never closed by the bus - consumers select on it alongside their ctx.Done. A nil
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
