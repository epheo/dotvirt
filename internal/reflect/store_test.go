package reflect

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
)

// TestStoreFiresOnChangeAndSyncs checks the watch->signal plumbing: each mutation
// fires onChange; the first Replace (initial relist) additionally fires onSynced
// exactly once, and a later relist must NOT re-fire it.
func TestStoreFiresOnChangeAndSyncs(t *testing.T) {
	var changes, syncs int
	s := NewStore(NewIndexer(), func() { changes++ }, func() { syncs++ })

	obj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "a"}}
	_ = s.Replace([]any{obj}, "1") // initial relist
	_ = s.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "b"}})
	_ = s.Replace([]any{obj}, "2") // a later relist must NOT re-fire onSynced

	if changes != 3 {
		t.Errorf("every mutation should fire onChange: want 3, got %d", changes)
	}
	if syncs != 1 {
		t.Errorf("onSynced should fire exactly once (first Replace), got %d", syncs)
	}
}

// TestStoreNilOnSynced confirms a signal-only store (no readiness callback) is safe.
func TestStoreNilOnSynced(t *testing.T) {
	var changes int
	s := NewStore(NewIndexer(), func() { changes++ }, nil)
	_ = s.Replace([]any{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "a"}}}, "1")
	if changes != 1 {
		t.Errorf("onChange should fire on Replace even with nil onSynced, got %d", changes)
	}
}

var _ cache.Store = NewStore(NewIndexer(), func() {}, nil) // must satisfy the reflector's store

// Every snapshot object carried its managedFields and kubectl's last-applied
// copy of the manifest, which no reader uses; the store drops both, typed or
// unstructured, and keeps every other annotation.
func TestStoreTrimsUnreadMetadata(t *testing.T) {
	managed := []metav1.ManagedFieldsEntry{{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationApply}}
	annotations := func() map[string]string {
		return map[string]string{lastApplied: `{"spec":{}}`, "keep": "yes"}
	}
	typed := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "a", ManagedFields: managed, Annotations: annotations()}}
	u := &unstructured.Unstructured{}
	u.SetName("b")
	u.SetManagedFields(managed)
	u.SetAnnotations(annotations())

	s := NewStore(NewIndexer(), func() {}, nil)
	_ = s.Replace([]any{typed}, "1")
	_ = s.Add(u)
	for _, key := range []string{"a", "b"} {
		obj, _, _ := s.GetByKey(key)
		m, err := meta.Accessor(obj)
		if err != nil {
			t.Fatal(err)
		}
		if m.GetManagedFields() != nil {
			t.Errorf("%s kept managedFields", key)
		}
		if a := m.GetAnnotations(); a[lastApplied] != "" || a["keep"] != "yes" {
			t.Errorf("%s annotations = %v, want only keep", key, a)
		}
	}
}

// Generation moves with writes in the namespaces asked about, and with a
// relist only where it changed something.
func TestGeneration(t *testing.T) {
	idx := NewIndexer()
	obj := func(ns, name string, rv ...string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{}}
		u.SetNamespace(ns)
		u.SetName(name)
		if len(rv) > 0 {
			u.SetResourceVersion(rv[0])
		}
		return u
	}
	gen := func() uint64 { return Generation(idx, "", "a") }
	last := gen()
	step := func(what string, moves bool) {
		t.Helper()
		if got := gen(); (got != last) != moves {
			t.Errorf("%s: moved %v, want %v", what, got != last, moves)
		}
		last = gen()
	}
	_ = idx.Add(obj("a", "x"))
	step("add in a", true)
	_ = idx.Update(obj("a", "x"))
	step("update in a", true)
	_ = idx.Add(obj("b", "y"))
	_ = idx.Update(obj("b", "y"))
	_ = idx.Delete(obj("b", "y"))
	step("writes in b", false)
	_ = idx.Add(obj("", "shared"))
	step("cluster-scoped add", true)
	_ = idx.Delete(obj("a", "x"))
	step("delete in a", true)
	_ = idx.Add(obj("a", "z", "1"))
	_ = idx.Add(obj("b", "y", "1"))
	last = gen()
	_ = idx.Replace([]any{obj("a", "z", "1"), obj("b", "y", "1"), obj("", "shared")}, "2")
	step("relist of nothing new", false)
	_ = idx.Replace([]any{obj("a", "z", "1"), obj("b", "y", "2"), obj("", "shared")}, "3")
	step("relist that moved b", false)
	_ = idx.Replace([]any{obj("a", "z", "2"), obj("b", "y", "2"), obj("", "shared")}, "4")
	step("relist that moved a", true)
	_ = idx.Replace([]any{obj("b", "y", "2"), obj("", "shared")}, "5")
	step("relist that emptied a", true)

	if Generation(cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{}), "a") != 0 {
		t.Error("a foreign indexer has no generation")
	}
}

// StoreGeneration moves with a write anywhere in the store, and with a
// relist only where it changed something.
func TestStoreGeneration(t *testing.T) {
	idx := NewIndexer()
	obj := func(ns, name, rv string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{}}
		u.SetNamespace(ns)
		u.SetName(name)
		u.SetResourceVersion(rv)
		return u
	}
	last := StoreGeneration(idx)
	step := func(what string, moves bool) {
		t.Helper()
		if got := StoreGeneration(idx); (got != last) != moves {
			t.Errorf("%s: moved %v, want %v", what, got != last, moves)
		}
		last = StoreGeneration(idx)
	}
	_ = idx.Add(obj("a", "x", "1"))
	step("add in a", true)
	_ = idx.Update(obj("b", "y", "1"))
	step("write in b", true)
	_ = idx.Add(obj("", "shared", "1"))
	step("cluster-scoped add", true)
	_ = idx.Delete(obj("a", "x", "1"))
	step("delete in a", true)
	_ = idx.Replace([]any{obj("b", "y", "1"), obj("", "shared", "1")}, "2")
	step("relist of nothing new", false)
	_ = idx.Replace([]any{obj("b", "y", "2"), obj("", "shared", "1")}, "3")
	step("relist that moved b", true)
	_ = idx.Replace([]any{obj("b", "y", "2")}, "4")
	step("relist that dropped the cluster-scoped object", true)

	if StoreGeneration(cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})) != 0 {
		t.Error("a foreign indexer has no generation")
	}
}
