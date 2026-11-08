package clusterstate

import (
	"sync"

	"k8s.io/client-go/tools/cache"
)

// nameSet is the sink of the all-namespaces watch: it keeps each namespace's
// name, never the object, for the tokens that may list namespaces
// cluster-wide (AllNamespaces). It publishes nothing: a namespace matters to
// a frame only through its objects, whose own kinds publish.
type nameSet struct {
	mu     sync.Mutex
	names  map[string]struct{}
	gen    uint64 // moves on every change of names
	synced func()
	listed bool
}

func newNameSet(synced func()) *nameSet {
	return &nameSet{names: map[string]struct{}{}, synced: synced}
}

func (n *nameSet) Add(obj any) error    { return n.put(obj) }
func (n *nameSet) Update(obj any) error { return n.put(obj) }

func (n *nameSet) put(obj any) error {
	name, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.names[name]; !ok {
		n.names[name] = struct{}{}
		n.gen++
	}
	return nil
}

func (n *nameSet) Delete(obj any) error {
	name, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.names[name]; ok {
		delete(n.names, name)
		n.gen++
	}
	return nil
}

func (n *nameSet) Replace(list []any, _ string) error {
	next := make(map[string]struct{}, len(list))
	for _, obj := range list {
		if name, err := cache.MetaNamespaceKeyFunc(obj); err == nil {
			next[name] = struct{}{}
		}
	}
	n.mu.Lock()
	n.names = next
	n.gen++
	first := !n.listed
	n.listed = true
	n.mu.Unlock()
	if first && n.synced != nil {
		n.synced()
	}
	return nil
}

func (n *nameSet) List() []any                        { return nil }
func (n *nameSet) ListKeys() []string                 { return nil }
func (n *nameSet) Get(any) (any, bool, error)         { return nil, false, nil }
func (n *nameSet) GetByKey(string) (any, bool, error) { return nil, false, nil }
func (n *nameSet) Resync() error                      { return nil }

func (n *nameSet) generation() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.gen
}

func (n *nameSet) copyInto(into map[string]bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for name := range n.names {
		into[name] = true
	}
}

// AllNamespaces is every namespace's name, the project namespaces included
// even before the all-namespaces watch reports them, as one map shared by
// every caller, which must not modify it. It is rebuilt only once either
// watch moved. Until the all-namespaces watch lists it holds the project
// namespaces alone.
func (s *State) AllNamespaces() map[string]bool {
	var gen [2]uint64
	if s.names != nil {
		gen[0] = s.names.generation()
	}
	gen[1] = s.nssGen.Load()
	s.allMu.Lock()
	defer s.allMu.Unlock()
	if s.all != nil && s.allGen == gen {
		return s.all
	}
	all := map[string]bool{}
	if s.names != nil {
		s.names.copyInto(all)
	}
	for _, name := range s.nss.ListKeys() {
		all[name] = true
	}
	s.all, s.allGen = all, gen
	return all
}
