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
