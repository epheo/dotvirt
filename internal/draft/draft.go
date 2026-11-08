// Package draft holds dotvirt's pending changesets: the edits, rendered
// manifests and removals a user staged but has not yet proposed. Drafts are
// keyed by (user, project) - each tenant gets an independent changeset per
// user - and persisted one JSON file per pair (<dir>/<user>/<project>.json) so
// they survive backend restarts.
package draft

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
)

// Kind distinguishes an edit of an existing VM, a brand-new VM, and the removal
// of an existing VM's manifest.
type Kind string

const (
	KindEdit   Kind = "edit"
	KindCreate Kind = "create"
	KindDelete Kind = "delete" // remove the VM's manifest from the repo (Argo prunes on merge)
)

// Resource is what an entry acts on. VM is the default (empty == vm, for
// back-compat with on-disk drafts); Network is a UDN, CUDN or NAD.
// It's part of the entry key so a VM and a network with the same ns/name don't
// clobber each other in the draft.
type Resource string

const (
	ResourceVM                         Resource = "vm"
	ResourceNetwork                    Resource = "network"                    // a project or shared network (UDN/CUDN)
	ResourceUplink                     Resource = "uplink"                     // a physical-network attachment (nmstate NNCP)
	ResourceNamespace                  Resource = "namespace"                  // a namespace (+ optional primary "VM Network")
	ResourceRoleBinding                Resource = "rolebinding"                // a tenant owners -> namespace-admin grant
	ResourceEgressFirewall             Resource = "egressfirewall"             // a namespace's outbound rules
	ResourceEgressIP                   Resource = "egressip"                   // a cluster-scoped fixed outbound IP pool
	ResourceExternalRoute              Resource = "externalroute"              // a cluster-scoped external next-hop route
	ResourceNetworkPolicy              Resource = "networkpolicy"              // a namespace's project rules
	ResourceAdminNetworkPolicy         Resource = "adminnetworkpolicy"         // cluster rules (ANP)
	ResourceBaselineAdminNetworkPolicy Resource = "baselineadminnetworkpolicy" // the baseline cluster rule (BANP)
	ResourceDRS                        Resource = "drs"                        // the cluster DRS (descheduler) install/config file set
	ResourceTemplate                   Resource = "template"                   // a VM template saved into a library's templates/ dir
)

// Atomic reports whether entries of this resource form one logical change that
// stages and unstages as a set - a partial DRS file set (e.g. the operator
// Subscription unstaged from under its KubeDescheduler CR) is not a meaningful
// proposal.
func (r Resource) Atomic() bool { return r == ResourceDRS }

// CreateLabel names a create of this resource in the Changes pane.
func (r Resource) CreateLabel() string { return r.row().CreateLabel }

// EditLabel names an in-place rewrite of this resource's manifest in the Changes
// pane (the git-declared object edited from its form).
func (r Resource) EditLabel() string { return r.row().EditLabel }

// Kinds are the Kubernetes kinds a resource's manifest can declare - how the
// delete and read-back paths find the object in git.
func (r Resource) Kinds() []string { return r.row().KindNames() }

// row is the resource's entry in the kind table; a word outside it (a draft
// written by another version) has no kinds and no labels.
func (r Resource) row() model.Resource {
	row, _ := model.LookupResource(string(r))
	return row
}

// Entry is one pending change, keyed by resource+namespace/name within its
// (user,project).
type Entry struct {
	Kind      Kind     `json:"kind"`
	Resource  Resource `json:"resource,omitempty"` // empty == vm
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`

	// SourceFile is the repo-relative manifest path: the file a create lands at,
	// an edit patches or replaces, a delete removes.
	SourceFile string `json:"sourceFile,omitempty"`

	// Edit fields (KindEdit): the change to apply to an existing manifest.
	Edit *model.VMEdit `json:"edit,omitempty"`

	// Instancetype is what the server read, under the stager's token, of the
	// instancetype Edit switches the VM to: which sizing fields it takes over.
	// Never the client's, and kept so propose and the view apply the read the
	// stage made.
	Instancetype *manifest.Target `json:"instancetype,omitempty"`

	// Manifest is the file content a create commits verbatim (rendered from a
	// form, captured from the cluster, or deployed from a template) or a
	// whole-file edit replaces. Every KindCreate carries one: a create without
	// it has nothing to commit and is dropped on load.
	Manifest string `json:"manifest,omitempty"`

	// FromWizard marks a VM manifest rendered from the New VM wizard, so the
	// draft view summarizes it by its sizing instead of presenting it as an
	// adoption (the default reading of a VM manifest create).
	FromWizard bool `json:"fromWizard,omitempty"`

	// FromTemplate names the library template ("<library>/<template>") a
	// verbatim-manifest VM create was rendered from, so the draft view can say
	// so instead of presenting it as an adoption.
	FromTemplate string `json:"fromTemplate,omitempty"`

	// FromVersion is the short commit hash a verbatim-manifest VM edit restores
	// the file to (the VM page's Restore), so the draft view can name it.
	FromVersion string `json:"fromVersion,omitempty"`

	// BaseBlob is the git blob SourceFile held on the base branch when an
	// entry replacing that file wholesale was staged. Its content was made
	// from that version, so propose refuses it once the file moves on:
	// committing it would silently revert the newer change.
	BaseBlob string `json:"baseBlob,omitempty"`
}

// Key is the stable identity used to dedupe/replace entries within a draft. VM
// keys keep the bare "ns/name" form (back-compat with on-disk drafts and the
// VM-scoped Unstage); other resources are prefixed so they can't collide.
func (e Entry) Key() string {
	if e.Resource == "" || e.Resource == ResourceVM {
		return e.Namespace + "/" + e.Name
	}
	return string(e.Resource) + ":" + e.Namespace + "/" + e.Name
}

// Store is the disk-persisted set of drafts, one per (user, project). It serves
// every user; isolation is by the (user, project) routing of each method, plus
// per-file persistence. In-memory state is loaded lazily per pair.
type Store struct {
	dir string

	mu     sync.Mutex
	drafts map[string]map[string]Entry // (user/project) -> (ns/name -> entry)
}

// Open roots a Store at dir (created on first write).
func Open(dir string) (*Store, error) {
	return &Store{dir: dir, drafts: map[string]map[string]Entry{}}, nil
}

// pairKey is the in-memory map key for a (user, project).
func pairKey(user, project string) string { return user + "\x00" + project }

// load reads a (user,project) draft file into memory if not already loaded.
// Caller holds s.mu.
func (s *Store) loadLocked(user, project string) (map[string]Entry, error) {
	pk := pairKey(user, project)
	if d, ok := s.drafts[pk]; ok {
		return d, nil
	}
	d := map[string]Entry{}
	p := s.path(user, project)
	data, err := os.ReadFile(p)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return nil, fmt.Errorf("read draft %s/%s: %w", user, project, err)
	default:
		var list []Entry
		if err := json.Unmarshal(data, &list); err != nil {
			// A parse failure must not wedge this (user, project) forever - a draft is
			// an unproposed shopping cart, not a source of truth. Quarantine the file
			// (kept for inspection, best-effort) and start fresh.
			if rerr := os.Rename(p, p+".corrupt"); rerr != nil && !os.IsNotExist(rerr) {
				return nil, fmt.Errorf("quarantine corrupt draft %s/%s: %w", user, project, rerr)
			}
			log.Printf("draft: %s is corrupt (%v); quarantined as .corrupt, starting fresh", p, err)
		} else {
			for _, e := range list {
				if e.Kind == KindCreate && e.Manifest == "" {
					// A create without a manifest has nothing to commit (an older
					// on-disk draft's unrendered spec); dropping it beats an empty file.
					log.Printf("draft: %s: dropping stale create %s (no manifest)", p, e.Key())
					continue
				}
				d[e.Key()] = e
			}
		}
	}
	s.drafts[pk] = d
	return d, nil
}

// path is the file backing one (user, project) draft. User and project are
// percent-escaped so identities containing '/' or ':' (e.g. "system:admin") map
// to a single safe path segment.
func (s *Store) path(user, project string) string {
	return filepath.Join(s.dir, safeSegment(user), safeSegment(project)+".json")
}

// safeSegment turns a user/project name into one safe path segment. url.PathEscape
// handles '/' and ':' but NOT '.', so a name of "." or ".." would traverse out of
// the draft dir; percent-encode those two to neutralize that. Normal names like
// "v1.2" stay intact.
func safeSegment(s string) string {
	switch s {
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	}
	return url.PathEscape(s)
}

// persistLocked writes a (user,project) draft to disk atomically, or removes the
// file when the draft is empty so emptied projects don't linger. Caller holds s.mu.
func (s *Store) persistLocked(user, project string, d map[string]Entry) error {
	if len(d) == 0 {
		return writeJSON(s.path(user, project), nil)
	}
	return writeJSON(s.path(user, project), sortedEntries(d))
}

// writeJSON replaces p with v atomically; a nil v removes p.
func writeJSON(p string, v any) error {
	if v == nil {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Apply runs fn on a copy of (user, project)'s draft, keyed by Entry.Key, and
// persists the result once. Memory moves only when the write lands, so a call
// either stages all of its change or none of it; an error from fn stages
// nothing.
func (s *Store) Apply(user, project string, fn func(d map[string]Entry) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.loadLocked(user, project)
	if err != nil {
		return err
	}
	next := maps.Clone(cur)
	if err := fn(next); err != nil {
		return err
	}
	if err := s.persistLocked(user, project, next); err != nil {
		return err
	}
	s.drafts[pairKey(user, project)] = next
	return nil
}

// Stage adds or replaces entries in (user, project)'s draft.
func (s *Store) Stage(user, project string, entries ...Entry) error {
	return s.Apply(user, project, func(d map[string]Entry) error {
		for _, e := range entries {
			d[e.Key()] = e
		}
		return nil
	})
}

// ReplaceResource makes entries the whole of resource r in (user, project)'s
// draft, reporting how many staged entries of r it dropped.
func (s *Store) ReplaceResource(user, project string, r Resource, entries ...Entry) (removed int, err error) {
	err = s.Apply(user, project, func(d map[string]Entry) error {
		removed = 0
		for k, e := range d {
			if e.Resource == r {
				delete(d, k)
				removed++
			}
		}
		for _, e := range entries {
			d[e.Key()] = e
		}
		return nil
	})
	return removed, err
}

// Remove unstages each of entries still staged exactly as given; one
// restaged since (a newer edit of the same object) stays.
func (s *Store) Remove(user, project string, entries ...Entry) error {
	return s.Apply(user, project, func(d map[string]Entry) error {
		for _, e := range entries {
			if cur, ok := d[e.Key()]; ok && reflect.DeepEqual(cur, e) {
				delete(d, e.Key())
			}
		}
		return nil
	})
}

// Unstage removes one entry (of the given resource) from (user,project)'s draft.
// No error if absent.
func (s *Store) Unstage(user, project string, resource Resource, namespace, name string) error {
	return s.Apply(user, project, func(d map[string]Entry) error {
		delete(d, Entry{Resource: resource, Namespace: namespace, Name: name}.Key())
		return nil
	})
}

// Clear empties (user,project)'s draft; persistLocked removes the now-empty file
// so emptied projects don't linger on disk.
func (s *Store) Clear(user, project string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.drafts, pairKey(user, project))
	return s.persistLocked(user, project, nil)
}

// List returns (user,project)'s entries sorted by key.
func (s *Store) List(user, project string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.loadLocked(user, project)
	if err != nil {
		return nil, err
	}
	return sortedEntries(d), nil
}

// Projects lists the projects user's drafts hold an entry in. The directory
// is the index: every write lands on disk before memory moves, and an emptied
// draft's file is removed. Each file is loaded all the same, as a draft
// holding only stale creates reads as empty.
func (s *Store) Projects(user string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := os.ReadDir(filepath.Join(s.dir, safeSegment(user)))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("list drafts of %s: %w", user, err)
	}
	var out []string
	for _, f := range files {
		seg, ok := strings.CutSuffix(f.Name(), ".json")
		if !ok || f.IsDir() {
			continue
		}
		project, err := url.PathUnescape(seg)
		if err != nil {
			continue
		}
		d, err := s.loadLocked(user, project)
		if err != nil {
			return nil, err
		}
		if len(d) > 0 {
			out = append(out, project)
		}
	}
	return out, nil
}

func sortedEntries(d map[string]Entry) []Entry {
	out := make([]Entry, 0, len(d))
	for _, e := range d {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// Proposal is what the open pull request of one (user, project) carries: the
// entries its branch was built from and the head dotvirt pushed. A re-propose
// rebuilds the branch from current base with these plus the draft, so the
// proposal is always replayed on the base it will merge into.
type Proposal struct {
	// Repo and Branch say where the record holds: a record for another repo
	// or branch is no record.
	Repo    string  `json:"repo"`
	Branch  string  `json:"branch"`
	PR      int     `json:"pr"` // 0: the branch is pushed but has no PR yet
	Head    string  `json:"head"`
	Entries []Entry `json:"entries"`
	// Pending is a push under way, written before it lands: if the record
	// write after it fails, the remote's head says which of the two holds.
	Pending *Pending `json:"pending,omitempty"`
}

// Pending is a pushed head and what it carries, not yet confirmed.
type Pending struct {
	Head    string  `json:"head"`
	Entries []Entry `json:"entries"`
}

// proposalPath sits beside the draft file. Draft files end in .json, so no
// project name maps onto it.
func (s *Store) proposalPath(user, project string) string {
	return filepath.Join(s.dir, safeSegment(user), safeSegment(project)+".proposal")
}

// Proposal reads (user, project)'s recorded proposal, ok=false when none. An
// unreadable record reads as none: the open PR is then refused, never rebuilt
// from a guess.
func (s *Store) Proposal(user, project string) (p Proposal, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.proposalPath(user, project))
	if os.IsNotExist(err) {
		return Proposal{}, false, nil
	}
	if err != nil {
		return Proposal{}, false, fmt.Errorf("read proposal %s/%s: %w", user, project, err)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		log.Printf("draft: proposal %s/%s is corrupt (%v); reading it as none", user, project, err)
		return Proposal{}, false, nil
	}
	return p, true, nil
}

// SetProposal records p as (user, project)'s open proposal.
func (s *Store) SetProposal(user, project string, p Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(s.proposalPath(user, project), p)
}

// DropProposal forgets (user, project)'s proposal: its PR merged or closed.
func (s *Store) DropProposal(user, project string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(s.proposalPath(user, project), nil)
}

// Pushed reports whether head is a push p knows: its head or the pending one.
func (p Proposal) Pushed(head string) bool {
	return head != "" && (head == p.Head || p.Pending != nil && head == p.Pending.Head)
}

// Settle makes p describe the remote's head: a pending push that landed
// becomes the record. False when head is neither push p knows.
func (p *Proposal) Settle(head string) bool {
	switch {
	case head != "" && head == p.Head:
	case p.Pending != nil && head != "" && head == p.Pending.Head:
		p.Head, p.Entries = p.Pending.Head, p.Pending.Entries
	default:
		return false
	}
	p.Pending = nil
	return true
}
