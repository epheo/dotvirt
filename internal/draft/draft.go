// Package draft holds dotvirt's pending changeset: VM edits and new-VM specs
// staged by the user but not yet committed. It persists to a JSON file so the
// draft survives backend restarts. A single shared draft (no per-user identity
// yet) is sufficient for a single-operator tool.
package draft

import (
	"encoding/json"
	"fmt"
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

// Entry is one pending change, keyed by namespace/name.
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

// Key is the stable identity used to dedupe/replace entries.
func (e Entry) Key() string { return e.Namespace + "/" + e.Name }

// Store is the disk-persisted draft changeset.
type Store struct {
	path string

	mu      sync.Mutex
	entries map[string]Entry // keyed by ns/name
}

// Open loads the draft from path (empty draft if the file doesn't exist).
func Open(path string) (*Store, error) {
	s := &Store{path: path, entries: map[string]Entry{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read draft %s: %w", s.path, err)
	}
	var list []Entry
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("parse draft %s: %w", s.path, err)
	}
	for _, e := range list {
		s.entries[e.Key()] = e
	}
	return nil
}

// persist writes the current entries to disk atomically. Caller holds s.mu.
func (s *Store) persist() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.listLocked(), "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Stage adds or replaces the entry for its VM and persists.
func (s *Store) Stage(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[e.Key()] = e
	return s.persist()
}

// Unstage removes a VM's entry and persists. No error if it wasn't staged.
func (s *Store) Unstage(namespace, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, namespace+"/"+name)
	return s.persist()
}

// Clear empties the draft and persists.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = map[string]Entry{}
	return s.persist()
}

// List returns the entries sorted by key for stable display.
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

// Len reports the number of pending entries.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *Store) listLocked() []Entry {
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}
