package clusterstate

import (
	"strings"
	"sync"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/pkg/tenancy"
)

// namespaceSink is the sink of the one Namespace watch, of every namespace.
// It keeps each name (AllNamespaces) and forwards the namespaces project
// selects to the project store, where one that stops matching reads as its
// delete: the store holds what a watch filtered by project would, without a
// second watch. The names publish nothing: a namespace's arrival alone
// rebuilds no frame, since what a frame shows of it arrives with its objects,
// whose own kinds publish.
//
// It also keeps each project's publish namespace (tenancy.PublishProject),
// which no project selects.
//
// A deleted namespace's grant levels are forgotten ForgetAfter later unless
// it came back (forget): its bus marks would otherwise outlive it.
type namespaceSink struct {
	project  labels.Selector
	projects cache.Indexer
	forget   func(name string)
	synced   func()
	now      func() time.Time

	mu        sync.Mutex
	names     map[string]struct{}
	published map[string]string // project -> its publish namespace
	gen       uint64            // moves on every change of names
	listed    bool
	gone      map[string]time.Time // deleted namespaces not forgotten yet, by deletion time
	swept     time.Time
}

// ForgetAfter is how long a deleted namespace's grant levels are kept: longer
// than any stamp of them serves (api.TestForgetOutlivesEveryGrantStamp), so
// no stamp read before its last move can still serve once they go
// (eventbus.Forget).
const ForgetAfter = 15 * time.Minute

func newNamespaceSink(project labels.Selector, projects cache.Indexer, forget func(string), synced func()) *namespaceSink {
	return &namespaceSink{project: project, projects: projects, forget: forget, synced: synced, now: time.Now,
		names: map[string]struct{}{}, published: map[string]string{}, gone: map[string]time.Time{}}
}

func (n *namespaceSink) selected(obj any) bool {
	m, err := apimeta.Accessor(obj)
	return err == nil && n.project.Matches(labels.Set(m.GetLabels()))
}

// publishOf is the project obj is the publish namespace of, "" for none.
func (n *namespaceSink) publishOf(name string, obj any) string {
	m, err := apimeta.Accessor(obj)
	if err != nil {
		return ""
	}
	return tenancy.PublishProject(name, m.GetLabels(), m.GetAnnotations(), n.selected(obj))
}

// setPublished records name as its project's publish namespace, or drops it
// as one. Callers hold mu.
func (n *namespaceSink) setPublished(name, project string) {
	if project != "" {
		n.published[project] = name
		return
	}
	if p := strings.TrimSuffix(name, tenancy.PublishSuffix); n.published[p] == name {
		delete(n.published, p)
	}
}

// publishNamespace is project's publish namespace, if the cluster holds it.
func (n *namespaceSink) publishNamespace(project string) (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ns, ok := n.published[project]
	return ns, ok
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
	n.setPublished(name, n.publishOf(name, obj))
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
	n.setPublished(name, "")
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
	published := map[string]string{}
	var selected []any
	for _, obj := range list {
		if name, err := cache.MetaNamespaceKeyFunc(obj); err == nil {
			next[name] = struct{}{}
			if p := n.publishOf(name, obj); p != "" {
				published[p] = name
			}
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
	n.published = published
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

// NamespacesGeneration moves whenever Namespaces or AllNamespaces may answer
// differently: a view built from them stays current while it holds still.
// Equality-only.
func (s *State) NamespacesGeneration() [2]uint64 {
	var gen [2]uint64
	if s.names != nil {
		gen[0] = s.names.generation()
	}
	gen[1] = s.nssGen.Load()
	return gen
}

// AllNamespaces is every namespace's name, the project namespaces included,
// as one map shared by every caller, which must not modify it. It is rebuilt
// only once the names or the project store moved.
func (s *State) AllNamespaces() map[string]bool {
	gen := s.NamespacesGeneration()
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

// PublishNamespace is project's publish namespace, when the cluster holds one
// (tenancy.PublishProject). It is in no project, so no Namespaces read finds
// it.
func (s *State) PublishNamespace(project string) (string, bool) {
	if s.names == nil {
		return "", false
	}
	return s.names.publishNamespace(project)
}
