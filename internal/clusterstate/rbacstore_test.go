package clusterstate

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func TestRBACStoreSignalsOnlyProjectNamespaces(t *testing.T) {
	s := stateWithIndexers()
	if err := s.nss.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "proj"}}); err != nil {
		t.Fatal(err)
	}
	var n int
	store := s.newRBACStore(func() { n++ })
	rb := func(ns string) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rb"}}
	}

	_ = store.Add(rb("other"))
	_ = store.Update(rb("other"))
	_ = store.Delete(cache.DeletedFinalStateUnknown{Key: "other/rb", Obj: rb("other")})
	if n != 0 {
		t.Fatalf("non-project RoleBinding signalled %d times, want 0", n)
	}

	_ = store.Add(rb("proj"))
	_ = store.Update(rb("proj"))
	_ = store.Delete(cache.DeletedFinalStateUnknown{Key: "proj/rb", Obj: rb("proj")})
	if n != 3 {
		t.Fatalf("project RoleBinding signalled %d times, want 3", n)
	}

	_ = store.Replace(nil, "1")
	if n != 4 {
		t.Fatalf("relist signalled %d total, want 4", n)
	}
}
