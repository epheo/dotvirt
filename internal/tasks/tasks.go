// Package tasks is the recent-activity feed behind the dock's Recent Tasks lane:
// the discrete acts every browser should see, not just the one that clicked. It
// holds two lanes in memory only - imperative runtime ops (recorded by the API
// handlers with the caller's identity) and PRs merged into a project's base
// branch (recorded by the forge webhook, re-derived by the refresher's poll).
// Deliberately no persistence: merges reseed from the forge after a restart, and
// the durable audit trail for ops is the cluster's own audit log - every op runs
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
// migrate, snapshot, cordon...). Namespace is empty for node-scoped ops.
type Op struct {
	Verb      string
	Namespace string
	Name      string
	// Desktop marks an act on the DeskuBe desktop Name; any other op in a
	// namespace acts on a VM.
	Desktop bool
	By      string
	OK      bool
	At      time.Time
}

// Merge is one PR merged into a project's base branch. RepoURL is the normalized
// clone URL - mapped to a project name at read time, under the reader's own
// visibility, so recording needs no project resolution. Branch is the PR's
// head: whose PR it was, exactly, where By is a lossy readable segment.
type Merge struct {
	RepoURL string
	Number  int
	URL     string
	Title   string
	By      string
	Branch  string
	// Head is the PR's head commit: it settles a draft dotvirt pushed there
	// with no PR (changeset.SettlePushed).
	Head string
	// Commit is the commit the forge says merged the PR into the base branch,
	// whatever its subject reads: the one Argo must run for it to be applied.
	Commit string
	At     time.Time
}

// Feed is the process-wide recent-tasks store. Safe for concurrent use.
type Feed struct {
	bus *eventbus.Bus

	mu     sync.Mutex
	ops    []Op                     // newest first
	merges map[string]map[int]Merge // repo -> PR number -> merge
}

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

// Visit calls op for each recorded op and merge for each retained merge, in no
// particular order, under the feed's lock: a reader that only folds the feed
// copies nothing. The callbacks must not call back into the feed.
func (f *Feed) Visit(op func(Op), merge func(Merge)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.ops {
		op(o)
	}
	for _, byNum := range f.merges {
		for _, m := range byNum {
			merge(m)
		}
	}
}

// RevertSegment is the branch segment under the proposed prefix that holds
// revert PRs: <prefix>/revert/<user>/<project>-<commit>. Named here, beside the
// attribution that must skip it, so the two cannot drift apart.
const RevertSegment = "revert"

// SeedSegment is the branch segment under the proposed prefix that holds the
// starter-template seed: <prefix>/_seed/starter-templates. No user segment
// starts with '_' or equals RevertSegment (the changeset's userSegment), so
// neither reserved segment ever names a user.
const SeedSegment = "_seed"

// UserEscape prefixes a first-segment user that would read as reserved (the
// changeset's userSegment): one leading UserEscape is stripped to read the
// name back. Only the first segment is escaped; a revert's user segment is
// the plain ref-safe name.
const UserEscape = "u-"

// MergeAuthor resolves who a merged PR belongs to. A proposal PR's poster is
// dotvirt's account, or the user's own forge account, named apart from the
// console's user - so the proposing user is read from the head branch, encoded
// (sanitized) as its first segment under the proposed prefix, one segment
// deeper for a revert. The seed belongs to no user: it stays its
// poster's, dotvirt's own account. A head outside that prefix is a human PR,
// attributed to its real poster.
func MergeAuthor(headRef, proposedPrefix, poster string) string {
	if proposedPrefix != "" {
		if rest, ok := strings.CutPrefix(headRef, proposedPrefix+"/"); ok {
			if strings.HasPrefix(rest, SeedSegment+"/") {
				return poster
			}
			rest, revert := strings.CutPrefix(rest, RevertSegment+"/")
			if user, _, found := strings.Cut(rest, "/"); found && user != "" {
				if !revert {
					user = strings.TrimPrefix(user, UserEscape)
				}
				return user
			}
		}
	}
	return poster
}
