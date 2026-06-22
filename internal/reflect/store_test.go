package reflect

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func newIndexer() cache.Indexer {
	return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
}

// TestStoreFiresOnChangeAndSyncs checks the watch→signal plumbing: each mutation
// fires onChange; the first Replace (initial relist) additionally fires onSynced
// exactly once, and a later relist must NOT re-fire it.
func TestStoreFiresOnChangeAndSyncs(t *testing.T) {
	var changes, syncs int
	s := NewStore(newIndexer(), func() { changes++ }, func() { syncs++ })

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
	s := NewStore(newIndexer(), func() { changes++ }, nil)
	_ = s.Replace([]any{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "a"}}}, "1")
	if changes != 1 {
		t.Errorf("onChange should fire on Replace even with nil onSynced, got %d", changes)
	}
}

var _ cache.Store = NewStore(newIndexer(), func() {}, nil) // must satisfy the reflector's store
