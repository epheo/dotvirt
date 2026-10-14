package clusterstate

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

// projectRBACStore drops RoleBinding events outside project namespaces before they
// reach the signal store: visibility is computed over project namespaces only, and
// cluster-wide RoleBinding churn (operators, CI) would otherwise flush every token's
// visible-set cache.
// A binding that lands before its namespace is labelled is still covered: the
// label change fires NamespaceChanged, which rbacVersion sums in.
type projectRBACStore struct {
	cache.Store
	isProject func(ns string) bool
}

func (s *projectRBACStore) relevant(obj any) bool {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	m, ok := obj.(metav1.Object)
	if !ok {
		return true // fail-safe: an unreadable event still invalidates
	}
	return s.isProject(m.GetNamespace())
}

func (s *projectRBACStore) Add(obj any) error {
	if !s.relevant(obj) {
		return nil
	}
	return s.Store.Add(obj)
}

func (s *projectRBACStore) Update(obj any) error {
	if !s.relevant(obj) {
		return nil
	}
	return s.Store.Update(obj)
}

func (s *projectRBACStore) Delete(obj any) error {
	if !s.relevant(obj) {
		return nil
	}
	return s.Store.Delete(obj)
}
