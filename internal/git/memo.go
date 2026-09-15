package git

import "sync"

// memoTable holds a repo's per-branch derived views (the declared index, the
// VM parse), each stamped with the branch head it reflects, so a view is built
// once per content change instead of once per read. Self-invalidating: when
// the fetcher advances the mirror the head moves and the next read misses. The
// head is read from the local mirror (no network), unlike headsSignature.
type memoTable struct {
	mu      sync.Mutex
	entries map[memoKey]memoEntry
}

type memoKey struct{ view, branch string }

type memoEntry struct {
	hash string
	val  any
}

// Memoized serves build's result for branch from r's memo while the branch head
// is unchanged; view names the derivation, so several views of one branch
// coexist. The head is read before building: a head that moves mid-build is
// stamped stale and rebuilt on the next read, never served for the new content.
// The value is shared with later callers: read only. A failed build is not
// memoized.
func Memoized[T any](r *Repo, view, branch string, build func() (T, error)) (T, error) {
	key := memoKey{view: view, branch: branch}
	hash := r.branchHash(branch)
	if hash != "" {
		r.memos.mu.Lock()
		e, ok := r.memos.entries[key]
		r.memos.mu.Unlock()
		if ok && e.hash == hash {
			return e.val.(T), nil
		}
	}
	val, err := build()
	if err != nil {
		var zero T
		return zero, err
	}
	if hash != "" {
		r.memos.mu.Lock()
		if r.memos.entries == nil {
			r.memos.entries = map[memoKey]memoEntry{}
		}
		r.memos.entries[key] = memoEntry{hash: hash, val: val}
		r.memos.mu.Unlock()
	}
	return val, nil
}
