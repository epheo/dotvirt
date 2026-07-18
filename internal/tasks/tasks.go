// Package tasks is the recent-activity feed behind the dock's Recent Tasks lane:
// the discrete acts every browser should see, not just the one that clicked. It
// holds two lanes in memory only — imperative runtime ops (recorded by the API
// handlers with the caller's identity) and PRs merged into a project's base
// branch (recorded by the forge webhook, re-derived by the refresher's poll).
// Deliberately no persistence: merges reseed from the forge after a restart, and
// the durable audit trail for ops is the cluster's own audit log — every op runs
// under the caller's token. Consumers ride TaskChanged on the bus and re-pull
// out-of-band, like the network catalog.
package tasks

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/eventbus"
)

// opsCap bounds the ops ring ("recent" is the last N acts, whatever their age).
const opsCap = 100

// MergeRetention bounds how far back the merged lane reaches; the refresher's
// forge query uses the same horizon, so the webhook path and the poll backstop
// agree on what "recent" means.
const MergeRetention = time.Hour

// Op is one imperative act performed through dotvirt as the caller (restart,
// migrate, snapshot, cordon…). Namespace is empty for node-scoped ops.
type Op struct {
	Verb      string
	Namespace string
	Name      string
	By        string
	OK        bool
	At        time.Time
}

// Merge is one PR merged into a project's base branch. RepoURL is the normalized
// clone URL — mapped to a project name at read time, under the reader's own
// visibility, so recording needs no project resolution.
type Merge struct {
	RepoURL string
	Number  int
	URL     string
	Title   string
	By      string
	At      time.Time
}

// Feed is the process-wide recent-tasks store. Safe for concurrent use.
type Feed struct {
	bus *eventbus.Bus

	mu     sync.Mutex
	ops    []Op                     // newest first
	merges map[string]map[int]Merge // repo -> PR number -> merge
}

// New builds an empty Feed publishing TaskChanged on bus.
func New(bus *eventbus.Bus) *Feed {
	return &Feed{bus: bus, merges: map[string]map[int]Merge{}}
}

// RecordOp prepends op to the ring and wakes subscribers.
func (f *Feed) RecordOp(op Op) {
	f.mu.Lock()
	f.ops = append([]Op{op}, f.ops...)
	if len(f.ops) > opsCap {
		f.ops = f.ops[:opsCap]
	}
	f.mu.Unlock()
	f.bus.Publish(eventbus.TaskChanged)
}

// RecordMerge upserts one merged PR, keyed by (repo, number) so the webhook's
// instant record and the refresher's later poll of the same PR coalesce into one
// row. Publishes only when the row is new or actually changed, so the steady-state
// poll never wakes anyone. Entries beyond MergeRetention are pruned on write.
func (f *Feed) RecordMerge(m Merge) {
	f.mu.Lock()
	byNum := f.merges[m.RepoURL]
	if byNum == nil {
		byNum = map[int]Merge{}
		f.merges[m.RepoURL] = byNum
	}
	prev, seen := byNum[m.Number]
	changed := !seen || prev != m
	byNum[m.Number] = m
	f.pruneLocked(time.Now().Add(-MergeRetention))
	f.mu.Unlock()
	if changed {
		f.bus.Publish(eventbus.TaskChanged)
	}
}

// pruneLocked drops merges older than cutoff. Caller holds f.mu.
func (f *Feed) pruneLocked(cutoff time.Time) {
	for repo, byNum := range f.merges {
		for n, m := range byNum {
			if m.At.Before(cutoff) {
				delete(byNum, n)
			}
		}
		if len(byNum) == 0 {
			delete(f.merges, repo)
		}
	}
}

// Ops returns the ring, newest first.
func (f *Feed) Ops() []Op {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Op, len(f.ops))
	copy(out, f.ops)
	return out
}

// Merges returns every retained merge, newest first.
func (f *Feed) Merges() []Merge {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Merge
	for _, byNum := range f.merges {
		for _, m := range byNum {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// MergeAuthor resolves who a merged PR belongs to. dotvirt's bot opens every
// proposal PR, so the poster is always the bot — the proposing user is encoded
// (sanitized) as the first head-branch segment under the proposed prefix. A head
// outside that prefix is a human PR, attributed to its real poster.
func MergeAuthor(headRef, proposedPrefix, poster string) string {
	if proposedPrefix != "" {
		if rest, ok := strings.CutPrefix(headRef, proposedPrefix+"/"); ok {
			if user, _, found := strings.Cut(rest, "/"); found && user != "" {
				return user
			}
		}
	}
	return poster
}
