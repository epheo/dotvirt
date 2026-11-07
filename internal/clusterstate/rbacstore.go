package clusterstate

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

// projectRBACStore is the RoleBinding reflector's sink. It retains nothing:
// dotvirt never reads RoleBindings, only their moves, and the reflector never
// reads its store back. It signals only bindings in project namespaces:
// visibility is computed over project namespaces only, and cluster-wide
// RoleBinding churn (operators, CI) would otherwise flush every token's
// visible-set cache.
// A binding that lands before its namespace is labelled is still covered: the
// label change fires NamespaceChanged, which rbacVersion sums in.
type projectRBACStore struct {
	isProject func(ns string) bool
	onChange  func()
}

func (s *projectRBACStore) signal(obj any) error {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	// fail-safe: an unreadable event still invalidates
	if m, ok := obj.(metav1.Object); !ok || s.isProject(m.GetNamespace()) {
		s.onChange()
	}
	return nil
}

func (s *projectRBACStore) Add(obj any) error    { return s.signal(obj) }
func (s *projectRBACStore) Update(obj any) error { return s.signal(obj) }
func (s *projectRBACStore) Delete(obj any) error { return s.signal(obj) }

func (s *projectRBACStore) Replace([]any, string) error {
	s.onChange()
	return nil
}

func (s *projectRBACStore) List() []any                        { return nil }
func (s *projectRBACStore) ListKeys() []string                 { return nil }
func (s *projectRBACStore) Get(any) (any, bool, error)         { return nil, false, nil }
func (s *projectRBACStore) GetByKey(string) (any, bool, error) { return nil, false, nil }
func (s *projectRBACStore) Resync() error                      { return nil }
