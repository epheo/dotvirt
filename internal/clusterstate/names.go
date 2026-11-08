package clusterstate

import (
	"sync"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
)

// namespaceSink is the sink of the one Namespace watch, of every namespace.
// It keeps each name (AllNamespaces) and forwards the namespaces project
// selects to the project store, where one that stops matching reads as its
// delete: the store holds what a watch filtered by project would, without a
// second watch. The names publish nothing: a namespace's arrival alone
// rebuilds no frame, since what a frame shows of it arrives with its objects,
// whose own kinds publish.
//
// A deleted namespace's grant levels are forgotten ForgetAfter later unless
// it came back (forget): its bus marks would otherwise outlive it.
type namespaceSink struct {
	project  labels.Selector
	projects cache.Indexer
	forget   func(name string)
	synced   func()
	now      func() time.Time

	mu     sync.Mutex
	names  map[string]struct{}
	gen    uint64 // moves on every change of names
	listed bool
	gone   map[string]time.Time // deleted namespaces not forgotten yet, by deletion time
	swept  time.Time
}

// ForgetAfter is how long a deleted namespace's grant levels are kept: longer
// than any stamp of them serves (api.TestForgetOutlivesEveryGrantStamp), so
// no stamp read before its last move can still serve once they go
// (eventbus.Forget).
const ForgetAfter = 15 * time.Minute

func newNamespaceSink(project labels.Selector, projects cache.Indexer, forget func(string), synced func()) *namespaceSink {
	return &namespaceSink{project: project, projects: projects, forget: forget, synced: synced, now: time.Now,
		names: map[string]struct{}{}, gone: map[string]time.Time{}}
}

func (n *namespaceSink) selected(obj any) bool {
	m, err := apimeta.Accessor(obj)
	return err == nil && n.project.Matches(labels.Set(m.GetLabels()))
}

func (n *namespaceSink) Add(obj any) error    { return n.put(obj) }
func (n *namespaceSink) Update(obj any) error { return n.put(obj) }

func (n *namespaceSink) put(obj any) error {
	name, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return err
	}
	n.mu.Lock()
	if _, ok := n.names[name]; !ok {
		n.names[name] = struct{}{}
		n.gen++
	}
	delete(n.gone, name)
	n.mu.Unlock()
	if n.selected(obj) {
		return n.projects.Update(obj)
	}
	if _, held, _ := n.projects.GetByKey(name); held {
		return n.projects.Delete(obj)
	}
	return nil
}

func (n *namespaceSink) Delete(obj any) error {
	name, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		return err
	}
	now := n.now()
	n.mu.Lock()
	if _, ok := n.names[name]; ok {
		delete(n.names, name)
		n.gen++
	}
	n.gone[name] = now
	due := n.sweep(now)
	n.mu.Unlock()
	n.forgetAll(due)
	if _, held, _ := n.projects.GetByKey(name); held {
		return n.projects.Delete(obj)
	}
	return nil
}

func (n *namespaceSink) Replace(list []any, rv string) error {
	next := make(map[string]struct{}, len(list))
	var selected []any
	for _, obj := range list {
		if name, err := cache.MetaNamespaceKeyFunc(obj); err == nil {
			next[name] = struct{}{}
		}
		if n.selected(obj) {
			selected = append(selected, obj)
		}
	}
	now := n.now()
	n.mu.Lock()
	for name := range n.names {
		if _, ok := next[name]; !ok {
			n.gone[name] = now
		}
	}
	for name := range next {
		delete(n.gone, name)
	}
	n.names = next
	n.gen++
	first := !n.listed
	n.listed = true
	due := n.sweep(now)
	n.mu.Unlock()
	n.forgetAll(due)
	if err := n.projects.Replace(selected, rv); err != nil {
		return err
	}
	if first && n.synced != nil {
		n.synced()
	}
	return nil
}

// sweep takes the namespaces gone longer than ForgetAfter, at most once a
// minute. Called under mu.
func (n *namespaceSink) sweep(now time.Time) []string {
	if now.Sub(n.swept) < time.Minute {
		return nil
	}
	n.swept = now
	var due []string
	for name, at := range n.gone {
		if now.Sub(at) >= ForgetAfter {
			due = append(due, name)
			delete(n.gone, name)
		}
	}
	return due
}

func (n *namespaceSink) forgetAll(names []string) {
	if n.forget == nil {
		return
	}
	for _, name := range names {
		n.forget(name)
	}
}

func (n *namespaceSink) List() []any                        { return nil }
func (n *namespaceSink) ListKeys() []string                 { return nil }
func (n *namespaceSink) Get(any) (any, bool, error)         { return nil, false, nil }
func (n *namespaceSink) GetByKey(string) (any, bool, error) { return nil, false, nil }
func (n *namespaceSink) Resync() error                      { return nil }

func (n *namespaceSink) generation() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.gen
}

func (n *namespaceSink) copyInto(into map[string]bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for name := range n.names {
		into[name] = true
	}
}

// AllNamespaces is every namespace's name, the project namespaces included,
// as one map shared by every caller, which must not modify it. It is rebuilt
// only once the names or the project store moved.
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
