// Package draft holds dotvirt's pending changesets: VM edits and new-VM specs
// staged by a user but not yet committed. Drafts are keyed by (user, project) —
// each tenant gets an independent changeset per user — and persisted one JSON
// file per pair (<dir>/<user>/<project>.json) so they survive backend restarts.
package draft

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/vmgen"
)

// Kind distinguishes an edit of an existing VM from a brand-new VM.
type Kind string

const (
	KindEdit   Kind = "edit"
	KindCreate Kind = "create"
)

// Entry is one pending change, keyed by namespace/name within its (user,project).
type Entry struct {
	Kind      Kind   `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	// Edit fields (KindEdit): the change to apply to an existing manifest.
	SourceFile string           `json:"sourceFile,omitempty"`
	Edit       *manifest.VMEdit `json:"edit,omitempty"`

	// Create fields (KindCreate): the wizard spec for a new VM.
	Spec *vmgen.Spec `json:"spec,omitempty"`
}

// Key is the stable identity used to dedupe/replace entries within a draft.
func (e Entry) Key() string { return e.Namespace + "/" + e.Name }

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
	data, err := os.ReadFile(s.path(user, project))
	switch {
	case os.IsNotExist(err):
		// fresh draft
	case err != nil:
		return nil, fmt.Errorf("read draft %s/%s: %w", user, project, err)
	default:
		var list []Entry
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("parse draft %s/%s: %w", user, project, err)
		}
		for _, e := range list {
			d[e.Key()] = e
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
// the draft dir; percent-encode leading dots to neutralize that (PathUnescape in
// ListProjects reverses it). The replace is anchored so only a segment that IS
// "."/".."(escaped) is rewritten, keeping normal names like "v1.2" intact.
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
	p := s.path(user, project)
	if len(d) == 0 {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sortedEntries(d), "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Stage adds or replaces an entry in (user,project)'s draft and persists.
func (s *Store) Stage(user, project string, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.loadLocked(user, project)
	if err != nil {
		return err
	}
	d[e.Key()] = e
	return s.persistLocked(user, project, d)
}

// Unstage removes a VM's entry from (user,project)'s draft. No error if absent.
func (s *Store) Unstage(user, project, namespace, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.loadLocked(user, project)
	if err != nil {
		return err
	}
	delete(d, namespace+"/"+name)
	return s.persistLocked(user, project, d)
}

// Clear empties (user,project)'s draft; persistLocked removes the now-empty file
// so ListProjects/Count don't keep enumerating emptied projects forever.
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

// Count reports the total pending entries across all of a user's projects (for
// the global "Changes" badge). It reads the user's directory from disk so it
// reflects projects not yet loaded this session.
func (s *Store) Count(user string) (int, error) {
	projects, err := s.ListProjects(user)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, p := range projects {
		entries, err := s.List(user, p)
		if err != nil {
			return 0, err
		}
		total += len(entries)
	}
	return total, nil
}

// ListProjects returns the projects a user has a draft for (by scanning their
// directory). Names are unescaped back to their original form.
func (s *Store) ListProjects(user string) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(s.dir, safeSegment(user)))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		project, err := url.PathUnescape(name[:len(name)-len(".json")])
		if err != nil {
			continue
		}
		out = append(out, project)
	}
	sort.Strings(out)
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
