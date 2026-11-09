package reflect

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Table is one kind's projection, for a snapshot whose reads filter per
// caller. Only what Decode keeps is held, so a field nobody may read never
// outlives the reflector's hand-off, and a write that moves none of it
// publishes nothing. It is a cache.Store because a reflector needs one;
// nothing reads it through that interface.
type Table[V any] struct {
	// Lock is the snapshot's, so one read sees every table at one moment.
	Lock *sync.RWMutex
	// Decode returns false to drop the object's row.
	Decode func(*unstructured.Unstructured) (V, bool)
	// Touch runs under Lock for each row that moved.
	Touch func(key string, old, now *V)
	// Key names a row when not the object's own name; empty skips the object.
	Key func(*unstructured.Unstructured) string
	// Publish runs after Lock is released, when something moved.
	Publish func()
	// Rows is read under Lock.
	Rows   map[string]V
	synced atomic.Bool
}

// NamespacedKey is a Key for a namespaced kind.
func NamespacedKey(u *unstructured.Unstructured) string {
	return u.GetNamespace() + "/" + u.GetName()
}

func (t *Table[V]) Add(obj any) error    { t.put(obj); return nil }
func (t *Table[V]) Update(obj any) error { t.put(obj); return nil }

func (t *Table[V]) keyOf(u *unstructured.Unstructured) string {
	if t.Key != nil {
		return t.Key(u)
	}
	return u.GetName()
}

func (t *Table[V]) Delete(obj any) error {
	if u, ok := obj.(*unstructured.Unstructured); ok && t.keyOf(u) != "" {
		t.Lock.Lock()
		moved := t.drop(t.keyOf(u))
		t.Lock.Unlock()
		if moved {
			t.Publish()
		}
	}
	return nil
}

func (t *Table[V]) put(obj any) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	key := t.keyOf(u)
	if key == "" {
		return
	}
	now, keep := t.Decode(u)
	t.Lock.Lock()
	var moved bool
	if keep {
		moved = t.set(key, now)
	} else {
		moved = t.drop(key)
	}
	t.Lock.Unlock()
	if moved {
		t.Publish()
	}
}

// set and drop run under Lock.
func (t *Table[V]) set(key string, now V) bool {
	if t.Rows == nil {
		t.Rows = map[string]V{}
	}
	old, had := t.Rows[key]
	if had && equality.Semantic.DeepEqual(old, now) {
		return false
	}
	t.Rows[key] = now
	if had {
		t.Touch(key, &old, &now)
	} else {
		t.Touch(key, nil, &now)
	}
	return true
}

func (t *Table[V]) drop(key string) bool {
	old, had := t.Rows[key]
	if !had {
		return false
	}
	delete(t.Rows, key)
	t.Touch(key, &old, nil)
	return true
}

// Replace applies a relist as the moves it amounts to, so one that changed
// nothing publishes nothing.
func (t *Table[V]) Replace(list []any, _ string) error {
	next := make(map[string]V, len(list))
	for _, obj := range list {
		if u, ok := obj.(*unstructured.Unstructured); ok && t.keyOf(u) != "" {
			if v, keep := t.Decode(u); keep {
				next[t.keyOf(u)] = v
			}
		}
	}
	t.Lock.Lock()
	moved := false
	for key := range t.Rows {
		if _, ok := next[key]; !ok {
			moved = t.drop(key) || moved
		}
	}
	for key, v := range next {
		moved = t.set(key, v) || moved
	}
	t.Lock.Unlock()
	first := t.synced.CompareAndSwap(false, true)
	if moved || first {
		t.Publish()
	}
	return nil
}

// Synced reports whether the first list landed: before it an empty table
// means unknown, not none.
func (t *Table[V]) Synced() bool { return t.synced.Load() }

// Clear forgets what a stopped watch left.
func (t *Table[V]) Clear() {
	t.Lock.Lock()
	t.Rows = nil
	t.Lock.Unlock()
	t.synced.Store(false)
}

func (t *Table[V]) Resync() error                      { return nil }
func (t *Table[V]) List() []any                        { return nil }
func (t *Table[V]) ListKeys() []string                 { return nil }
func (t *Table[V]) Get(any) (any, bool, error)         { return nil, false, nil }
func (t *Table[V]) GetByKey(string) (any, bool, error) { return nil, false, nil }

// RunWhile keeps a module's watches running while on holds, and returns at
// once. ready gates the start; off stops them, so an uninstalled module leaves
// no watch retrying.
func RunWhile(ctx context.Context, pace time.Duration, ready, on func() bool, start func(context.Context), stop func()) {
	go func() {
		t := time.NewTicker(pace)
		defer t.Stop()
		until := func(cond func() bool) bool {
			for !cond() {
				select {
				case <-ctx.Done():
					return false
				case <-t.C:
				}
			}
			return true
		}
		for until(ready) {
			run, cancel := context.WithCancel(ctx)
			start(run)
			off := until(func() bool { return !on() })
			cancel()
			stop()
			if !off {
				return
			}
		}
	}()
}
