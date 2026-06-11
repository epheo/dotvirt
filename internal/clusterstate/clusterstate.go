// Package clusterstate maintains dotvirt's single source of live cluster truth.
//
// The read path used to fetch this per request, per subscriber: every inventory
// build issued a namespace LIST, a GET per namespace, and a VM+VMI LIST per
// namespace — all under the user's token, all repeated on every change. A handful
// of viewers plus a burst of VM events exhausted client-go's per-client rate
// limiter and wedged the server for minutes.
//
// Instead, three reflectors run ONCE under dotvirt's ServiceAccount and keep
// in-memory indexers of VirtualMachines, VirtualMachineInstances, and the
// project-labeled Namespaces — the data is identical for every tenant, so it
// belongs in one shared snapshot, not in N per-user fetches. A read becomes a
// pure in-memory filter of this snapshot through the caller's visible-namespace
// set (computed elsewhere, per token): the cluster is never touched on the read
// path. This is the same idea DriftCache already applied to Argo drift, finished
// for live state and topology and fed by watches rather than polled.
//
// Authorization is unchanged: the snapshot is global truth, but a user only ever
// sees the namespaces their own RBAC grants — the filter is the security gate.
package clusterstate

import (
	"context"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/project"
)

// LiveVM is one VM's actual state, keyed by "namespace/name" for merging into the
// git-derived inventory. Mirrors cluster.LiveVM (kept distinct so clusterstate
// owns its read DTO and callers don't reach back into the fetch layer).
type LiveVM struct {
	Phase    string
	GuestIP  string
	NodeName string
	Ready    bool
}

// State is the SA-maintained snapshot. Build with New, start with Run; reads
// (LiveVMs, Namespaces) are lock-free indexer scans safe for concurrent callers.
type State struct {
	vms  cache.Indexer
	vmis cache.Indexer
	nss  cache.Indexer

	specs []reflectorSpec // reflector wiring, built in New, started in Run

	version atomic.Uint64
	synced  atomic.Int32    // reflectors whose initial LIST (first Replace) has landed
	changed chan<- struct{} // coalesced "snapshot moved" signal for the hub
}

// New builds the snapshot's reflectors over sa (dotvirt's ServiceAccount client),
// watching the namespaces labeled projectLabel. changed receives a coalesced
// signal whenever the snapshot moves, so the hub re-broadcasts; it is optional
// (nil disables signalling, e.g. in tests).
func New(sa *cluster.Client, projectLabel string, changed chan<- struct{}) *State {
	s := &State{changed: changed}
	s.vms = newIndexer()
	s.vmis = newIndexer()
	s.nss = newIndexer()
	s.specs = []reflectorSpec{
		{s.vms, &kubevirtcorev1.VirtualMachine{}, sa.VMListWatch()},
		{s.vmis, &kubevirtcorev1.VirtualMachineInstance{}, sa.VMIListWatch()},
		{s.nss, &corev1.Namespace{}, sa.NamespaceListWatch(projectLabel)},
	}
	return s
}

type reflectorSpec struct {
	store    cache.Indexer
	expected any
	lw       cache.ListerWatcher
}

// Run starts one reflector per resource; each owns its own relist/backoff and
// stops when ctx is cancelled. Returns immediately — call WaitForSync to block
// until the initial LIST has populated the snapshot.
func (s *State) Run(ctx context.Context) {
	for _, spec := range s.specs {
		store := &countingStore{Indexer: spec.store, on: s.bump, synced: func() { s.synced.Add(1) }}
		r := cache.NewReflector(spec.lw, spec.expected, store, 0)
		go r.Run(ctx.Done())
	}
}

// newIndexer builds a namespace-keyed store for one resource.
func newIndexer() cache.Indexer {
	return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
}

// WaitForSync blocks until each reflector's initial LIST has landed (all three
// stores have seen their first Replace), or ctx is done. Callers use it so the
// first inventory served isn't empty. Returns ctx.Err() on cancellation, nil once
// synced.
func (s *State) WaitForSync(ctx context.Context) error {
	for s.synced.Load() < int32(len(s.specs)) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

// bump records that the snapshot moved and signals the hub (coalesced).
func (s *State) bump() {
	s.version.Add(1)
	if s.changed == nil {
		return
	}
	select {
	case s.changed <- struct{}{}:
	default: // a signal is already pending; the hub recomputes the whole snapshot anyway
	}
}

// Version is a monotonic counter that ticks on every snapshot change. The hub can
// compare it to skip a broadcast when nothing moved (e.g. a resync with no delta).
func (s *State) Version() uint64 { return s.version.Load() }

// LiveVMs returns the current live state of every VM in the snapshot, keyed by
// "namespace/name". A VM with no running VMI is present with a zero (stopped)
// LiveVM; a VMI supplies phase/IP/node. Pure in-memory — no cluster call.
func (s *State) LiveVMs() map[string]LiveVM {
	out := map[string]LiveVM{}
	for _, obj := range s.vms.List() {
		vm, ok := obj.(*kubevirtcorev1.VirtualMachine)
		if !ok {
			continue
		}
		out[vm.Namespace+"/"+vm.Name] = LiveVM{} // exists, no running instance
	}
	for _, obj := range s.vmis.List() {
		vmi, ok := obj.(*kubevirtcorev1.VirtualMachineInstance)
		if !ok {
			continue
		}
		out[vmi.Namespace+"/"+vmi.Name] = liveFromVMI(vmi)
	}
	return out
}

// Namespaces returns the project-labeled namespaces in the snapshot as the
// resolver's input type (Name + labels/annotations), so the read path and the
// exporter feed project.Resolve directly. Pure in-memory — no cluster call.
func (s *State) Namespaces() []project.Namespace {
	objs := s.nss.List()
	out := make([]project.Namespace, 0, len(objs))
	for _, obj := range objs {
		ns, ok := obj.(*corev1.Namespace)
		if !ok {
			continue
		}
		out = append(out, project.Namespace{Name: ns.Name, Labels: ns.Labels, Annotations: ns.Annotations})
	}
	return out
}

func liveFromVMI(vmi *kubevirtcorev1.VirtualMachineInstance) LiveVM {
	live := LiveVM{Phase: string(vmi.Status.Phase), NodeName: vmi.Status.NodeName}
	if len(vmi.Status.Interfaces) > 0 {
		live.GuestIP = vmi.Status.Interfaces[0].IP
	}
	for _, cond := range vmi.Status.Conditions {
		if cond.Type == kubevirtcorev1.VirtualMachineInstanceReady {
			live.Ready = cond.Status == corev1.ConditionTrue
		}
	}
	return live
}
