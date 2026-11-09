package clusterstate

import (
	"slices"
	"strings"
	"sync"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/tools/cache"
)

// bindingIndex is where bindings of one kind name each subject: by namespace
// for RoleBindings, under "" for ClusterRoleBindings. It narrows which
// namespaces a token is asked about: the apiserver's review stays the answer,
// so a miss can hide a namespace and never reveal one.
type bindingIndex struct {
	mu        sync.RWMutex
	bySubject map[string]map[string]int // subject -> namespace -> bindings naming it there
	byBinding map[string]boundSubjects
}

type boundSubjects struct {
	namespace string
	subjects  []string
}

func newBindingIndex() *bindingIndex {
	return &bindingIndex{bySubject: map[string]map[string]int{}, byBinding: map[string]boundSubjects{}}
}

func userSubject(name string) string  { return "u:" + name }
func groupSubject(name string) string { return "g:" + name }

// subjectKey spells a subject as a token presents it: a ServiceAccount is a
// username, and one with no namespace is the binding's own.
func subjectKey(bindingNS string, sub rbacv1.Subject) string {
	switch sub.Kind {
	case rbacv1.GroupKind:
		return groupSubject(sub.Name)
	case rbacv1.ServiceAccountKind:
		ns := sub.Namespace
		if ns == "" {
			ns = bindingNS
		}
		return userSubject("system:serviceaccount:" + ns + ":" + sub.Name)
	}
	return userSubject(sub.Name)
}

// bindingOf reads either binding kind; its key is the watch's own.
func bindingOf(obj any) (key, namespace string, subjects []rbacv1.Subject, ok bool) {
	switch b := obj.(type) {
	case *rbacv1.RoleBinding:
		return b.Namespace + "/" + b.Name, b.Namespace, b.Subjects, true
	case *rbacv1.ClusterRoleBinding:
		return b.Name, "", b.Subjects, true
	}
	return "", "", nil, false
}

func (x *bindingIndex) put(obj any) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.putLocked(obj)
}

func (x *bindingIndex) putLocked(obj any) {
	key, namespace, subs, ok := bindingOf(obj)
	if !ok {
		return
	}
	x.dropLocked(key)
	subjects := make([]string, 0, len(subs))
	for _, sub := range subs {
		k := subjectKey(namespace, sub)
		subjects = append(subjects, k)
		if x.bySubject[k] == nil {
			x.bySubject[k] = map[string]int{}
		}
		x.bySubject[k][namespace]++
	}
	x.byBinding[key] = boundSubjects{namespace: namespace, subjects: subjects}
}

func (x *bindingIndex) dropLocked(key string) {
	held, ok := x.byBinding[key]
	if !ok {
		return
	}
	delete(x.byBinding, key)
	for _, k := range held.subjects {
		if x.bySubject[k][held.namespace]--; x.bySubject[k][held.namespace] <= 0 {
			delete(x.bySubject[k], held.namespace)
		}
		if len(x.bySubject[k]) == 0 {
			delete(x.bySubject, k)
		}
	}
}

func (x *bindingIndex) replace(list []any) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.bySubject, x.byBinding = map[string]map[string]int{}, make(map[string]boundSubjects, len(list))
	for _, obj := range list {
		x.putLocked(obj)
	}
}

func (x *bindingIndex) namespaces(username string, groups []string) map[string]bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := map[string]bool{}
	for ns := range x.bySubject[userSubject(username)] {
		out[ns] = true
	}
	for _, g := range groups {
		for ns := range x.bySubject[groupSubject(g)] {
			out[ns] = true
		}
	}
	return out
}

// indexedBindings feeds the index before the signal, so whoever a published
// move wakes finds the index already current.
type indexedBindings struct {
	*rbacSignal
	index *bindingIndex
}

func (b indexedBindings) Add(obj any) error {
	b.index.put(obj)
	return b.rbacSignal.Add(obj)
}

func (b indexedBindings) Update(obj any) error {
	b.index.put(obj)
	return b.rbacSignal.Update(obj)
}

func (b indexedBindings) Delete(obj any) error {
	if key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj); err == nil {
		b.index.mu.Lock()
		b.index.dropLocked(key)
		b.index.mu.Unlock()
	}
	return b.rbacSignal.Delete(obj)
}

func (b indexedBindings) Replace(list []any, rv string) error {
	b.index.replace(list)
	return b.rbacSignal.Replace(list, rv)
}

// BoundNamespaces are the namespaces whose RoleBindings name username or one of
// groups. ok=false until the RoleBinding watch is complete and healthy: every
// namespace must then be asked.
func (s *State) BoundNamespaces(username string, groups []string) (map[string]bool, bool) {
	if username == "" || s.rbSink == nil || !s.rbSink.listed.Load() || !s.healthy[s.rbHealth].Load() {
		return nil, false
	}
	return s.bindings.namespaces(username, groups), true
}

// ClusterBound reports whether a ClusterRoleBinding may reach username in a
// single namespace, which a review naming the object sees through
// resourceNames and the RoleBinding index cannot. True while that watch is
// incomplete. system: groups do not count: they hold nearly everyone, and what
// they grant is cluster-wide or nothing.
func (s *State) ClusterBound(username string, groups []string) bool {
	if username == "" || !s.RBACWatched() {
		return true
	}
	named := slices.DeleteFunc(slices.Clone(groups), func(g string) bool { return strings.HasPrefix(g, "system:") })
	return len(s.clusterBindings.namespaces(username, named)) > 0
}
