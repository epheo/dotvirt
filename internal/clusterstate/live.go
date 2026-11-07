package clusterstate

import (
	"maps"
	stdreflect "reflect"
	"sync"
	"sync/atomic"

	"k8s.io/client-go/tools/cache"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/reflect"
)

// LiveView is every VM's live state by namespace, then name: a VM with no VMI
// holds a zero (stopped) LiveVM, a VMI supplies phase/IP/node. One view is
// shared by every identity's frame, so it and the LiveVMs in it are read-only.
type LiveView map[string]map[string]LiveVM

// liveIndex keeps the LiveView current as the VM and VMI stores move, so a frame
// reads one pointer instead of rebuilding the map per identity. It publishes only
// when a derived LiveVM changed: KubeVirt rewrites VM and VMI status (probe
// times, guest-agent polls) far more often than anything a frame shows.
type liveIndex struct {
	vms, vmis cache.Indexer
	onChange  func()

	// mu serializes the VM and VMI reflector goroutines' copy-on-write updates;
	// readers only load view.
	mu   sync.Mutex
	view atomic.Pointer[LiveView]
}

func newLiveIndex(vms, vmis cache.Indexer, onChange func()) *liveIndex {
	x := &liveIndex{vms: vms, vmis: vmis, onChange: onChange}
	x.view.Store(&LiveView{})
	return x
}

func (x *liveIndex) load() LiveView { return *x.view.Load() }

// store wraps idx (the VM or the VMI indexer) as a reflector sink that keeps
// the view current.
func (x *liveIndex) store(idx cache.Indexer, onSynced func()) cache.Store {
	return liveStore{Indexer: reflect.NewStore(idx, func() {}, onSynced), live: x}
}

// derive is key's view entry recomputed from both indexers. Recomputing from both,
// rather than applying the one event, lets the two reflectors interleave freely:
// each mutates its indexer before taking mu, so the later update sees both.
func (x *liveIndex) derive(key string) (LiveVM, bool) {
	if obj, ok, _ := x.vmis.GetByKey(key); ok {
		if vmi, ok := obj.(*kubevirtcorev1.VirtualMachineInstance); ok {
			return liveFromVMI(vmi), true
		}
	}
	_, ok, _ := x.vms.GetByKey(key)
	return LiveVM{}, ok
}

func (x *liveIndex) touch(obj any) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		return
	}
	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return
	}
	x.mu.Lock()
	live, present := x.derive(key)
	changed := x.setLocked(ns, name, live, present)
	x.mu.Unlock()
	if changed {
		x.onChange()
	}
}

// setLocked swaps in a view with ns/name set (or removed), copying only the outer
// map and ns's own: a published view is never written again.
func (x *liveIndex) setLocked(ns, name string, live LiveVM, present bool) bool {
	cur := x.load()
	old, had := cur[ns][name]
	if had == present && (!present || stdreflect.DeepEqual(old, live)) {
		return false
	}
	next := maps.Clone(cur)
	inner := maps.Clone(cur[ns])
	if present {
		if inner == nil {
			inner = map[string]LiveVM{}
		}
		inner[name] = live
		next[ns] = inner
	} else {
		delete(inner, name)
		if len(inner) == 0 {
			delete(next, ns)
		} else {
			next[ns] = inner
		}
	}
	x.view.Store(&next)
	return true
}

// rebuild recomputes the whole view after a relist, which replaces a store
// wholesale without per-object events.
func (x *liveIndex) rebuild() {
	next := LiveView{}
	put := func(ns, name string, live LiveVM) {
		if next[ns] == nil {
			next[ns] = map[string]LiveVM{}
		}
		next[ns][name] = live
	}
	x.mu.Lock()
	for _, obj := range x.vms.List() {
		if vm, ok := obj.(*kubevirtcorev1.VirtualMachine); ok {
			put(vm.Namespace, vm.Name, LiveVM{})
		}
	}
	for _, obj := range x.vmis.List() {
		if vmi, ok := obj.(*kubevirtcorev1.VirtualMachineInstance); ok {
			put(vmi.Namespace, vmi.Name, liveFromVMI(vmi))
		}
	}
	changed := !stdreflect.DeepEqual(x.load(), next)
	if changed {
		x.view.Store(&next)
	}
	x.mu.Unlock()
	if changed {
		x.onChange()
	}
}

// liveStore is the reflector sink of the VM or the VMI indexer.
type liveStore struct {
	cache.Indexer
	live *liveIndex
}

func (c liveStore) Add(obj any) error {
	if err := c.Indexer.Add(obj); err != nil {
		return err
	}
	c.live.touch(obj)
	return nil
}

func (c liveStore) Update(obj any) error {
	if err := c.Indexer.Update(obj); err != nil {
		return err
	}
	c.live.touch(obj)
	return nil
}

func (c liveStore) Delete(obj any) error {
	if err := c.Indexer.Delete(obj); err != nil {
		return err
	}
	c.live.touch(obj)
	return nil
}

func (c liveStore) Replace(list []any, rv string) error {
	if err := c.Indexer.Replace(list, rv); err != nil {
		return err
	}
	c.live.rebuild()
	return nil
}
