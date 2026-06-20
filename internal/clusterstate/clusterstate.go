// Package clusterstate maintains dotvirt's single source of live cluster truth.
//
// The read path used to fetch this per request, per subscriber: every inventory
// build issued a namespace LIST, a GET per namespace, and a VM+VMI LIST per
// namespace — all under the user's token, all repeated on every change. A handful
// of viewers plus a burst of VM events exhausted client-go's per-client rate
// limiter and wedged the server for minutes.
//
// Instead, reflectors run ONCE under dotvirt's ServiceAccount and keep in-memory
// indexers of VirtualMachines, VirtualMachineInstances, and the project-labeled
// Namespaces — the data is identical for every tenant, so it belongs in one shared
// snapshot, not in N per-user fetches. A read becomes a pure in-memory filter of
// this snapshot through the caller's visible-namespace set (computed elsewhere, per
// token): the cluster is never touched on the read path. This is the reflector
// model the argo drift snapshot also follows — fed by watches, not polled.
//
// A fourth, signal-only reflector watches RoleBindings: dotvirt never reads them,
// but a RoleBinding move can change which namespaces a token may see, so it
// publishes RBACChanged to invalidate the per-token visible-set cache promptly.
//
// On any mutation a reflector publishes its kind to the shared event bus, which the
// hub (and exporter) subscribe to; the read path doesn't diff anything.
//
// Authorization is unchanged: the snapshot is global truth, but a user only ever
// sees the namespaces their own RBAC grants — the filter is the security gate.
package clusterstate

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/tools/cache"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/reflect"
)

// LiveVM is one VM's actual state, keyed by "namespace/name" for merging into the
// git-derived inventory — clusterstate's own read DTO, derived from the watched
// VMI objects (inventory.applyLive copies it onto each model.VM).
type LiveVM struct {
	Phase    string
	GuestIP  string   // primary interface IP (the inventory grid's IP column)
	IPs      []string // every guest-reported IP, for the detail view
	NodeName string

	// Guest-agent + runtime facts for the VM summary dashboard. Empty when the VMI
	// isn't running or the guest agent isn't reporting.
	OS           string    // guest OS pretty name, e.g. "Fedora Linux 40 (Cloud Edition)"
	MemoryActual string    // current guest memory (hotplug-aware), e.g. "1Gi"
	StartedAt    time.Time // when the VMI entered Running, for uptime

	Paused bool // VMI Paused condition is true (phase stays Running while paused)
	Ready  bool

	// Interfaces is each VMI interface's runtime address info, merged onto the
	// VM's manifest-declared NIC of the same name for the detail view's per-NIC
	// IP/MAC (vCenter's per-adapter address columns).
	Interfaces []LiveNIC

	// Migration mirrors the VMI's MigrationState when one exists: the live (or
	// just-finished) node-to-node move. KubeVirt keeps the last migration's state
	// on the VMI, so a nil check distinguishes "never migrated" from "idle".
	Migration *Migration
}

// LiveNIC is one VMI interface's runtime address info (name matches the VM's
// manifest NIC name).
type LiveNIC struct {
	Name string
	MAC  string
	IP   string
}

// Migration is a VM's node-to-node move — vCenter's vMotion progress. Active
// while neither Completed nor Failed is set.
type Migration struct {
	SourceNode string
	TargetNode string
	StartedAt  time.Time
	EndedAt    time.Time
	Completed  bool
	Failed     bool
}

// State is the SA-maintained snapshot. Build with New, start with Run; reads
// (LiveVMs, Namespaces) are lock-free indexer scans safe for concurrent callers.
type State struct {
	vms  cache.Indexer
	vmis cache.Indexer
	nss  cache.Indexer
	// RoleBindings are watched too, but only as an RBACChanged signal — via a
	// retain-nothing signal store, so there's no indexer for them here.

	specs []reflectorSpec // reflector wiring, built in New, started in Run

	// Per-store readiness: each reflector's initial LIST (first Replace) landing.
	// Tracked per store (not a single counter) so a consumer gates on exactly the
	// stores it reads — the exporter must not stall on a failing VMI reflector, and
	// nobody waits on the signal-only RoleBinding reflector. Lock-free ExportReady
	// reads on the hot path.
	vmsSynced, vmisSynced, nssSynced atomic.Bool
	// allSynced is closed once the three readable stores have all synced, so
	// WaitForSync blocks deterministically instead of polling.
	syncedOnce sync.Once
	allSynced  chan struct{}

	bus *eventbus.Bus // reflectors publish their kind here on every mutation
}

// New builds the snapshot's reflectors over sa (dotvirt's ServiceAccount client),
// watching the namespaces labeled projectLabel. Each reflector publishes its kind to
// bus whenever the snapshot moves, so the inventory hub rebuilds (and the exporter
// re-exports); bus is optional (nil disables signalling, e.g. in tests).
func New(sa *cluster.Client, projectLabel string, bus *eventbus.Bus) *State {
	s := &State{bus: bus, allSynced: make(chan struct{})}
	s.vms = newIndexer()
	s.vmis = newIndexer()
	s.nss = newIndexer()

	vmSpec := func() { bus.Publish(eventbus.VMSpecChanged) }
	live := func() { bus.Publish(eventbus.LiveChanged) }
	namespace := func() { bus.Publish(eventbus.NamespaceChanged) }
	rbac := func() { bus.Publish(eventbus.RBACChanged) }
	s.specs = []reflectorSpec{
		// vms: VMSpecChanged is gated on metadata.generation by vmSpecStore, so a
		// status-only VM write fires only LiveChanged — the exporter (which depends on
		// VMSpecChanged) doesn't wake on VM-status heartbeats.
		{newVMSpecStore(s.vms, vmSpec, live, func() { s.vmsSynced.Store(true); s.checkSynced() }), &kubevirtcorev1.VirtualMachine{}, sa.VMListWatch()},
		{reflect.NewStore(s.vmis, live, func() { s.vmisSynced.Store(true); s.checkSynced() }), &kubevirtcorev1.VirtualMachineInstance{}, sa.VMIListWatch()},
		// A namespace move is both a topology change (inventory) and a visibility
		// change; NamespaceChanged is summed into both the inventory and RBAC versions.
		{reflect.NewStore(s.nss, namespace, func() { s.nssSynced.Store(true); s.checkSynced() }), &corev1.Namespace{}, sa.NamespaceListWatch(projectLabel)},
		// Signal-only: a retain-nothing store (never read), so the cluster-wide
		// RoleBinding watch costs no per-object memory — it only nudges visibility.
		{reflect.NewSignalStore(rbac, nil), &rbacv1.RoleBinding{}, sa.RoleBindingListWatch()},
	}
	return s
}

type reflectorSpec struct {
	store    cache.Store // already wrapped to fire the right Publishes + readiness
	expected any
	lw       cache.ListerWatcher
}

// Run starts one reflector per resource; each owns its own relist/backoff and
// stops when ctx is cancelled. Returns immediately — call WaitForSync to block
// until the initial LIST has populated the snapshot.
func (s *State) Run(ctx context.Context) {
	for _, spec := range s.specs {
		r := cache.NewReflector(spec.lw, spec.expected, spec.store, 0)
		go r.Run(ctx.Done())
	}
}

// newIndexer builds a namespace-keyed store for one resource.
func newIndexer() cache.Indexer {
	return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
}

// WaitForSync blocks until every READABLE reflector's initial LIST has landed (the
// VM, VMI and namespace stores — not the signal-only RoleBinding watch), or ctx is
// done. Deterministic: it waits on a channel closed by the last store to sync, not a
// poll. Returns ctx.Err() on cancellation, nil once synced.
func (s *State) WaitForSync(ctx context.Context) error {
	select {
	case <-s.allSynced:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// checkSynced closes allSynced once all three readable stores have landed their
// initial LIST. Invoked from each readable reflector's onSynced (each fires exactly
// once); the sync.Once makes the close idempotent.
func (s *State) checkSynced() {
	if s.vmsSynced.Load() && s.vmisSynced.Load() && s.nssSynced.Load() {
		s.syncedOnce.Do(func() { close(s.allSynced) })
	}
}

// ExportReady reports whether the stores the exporter reads — VMs and namespaces —
// have synced. The exporter prunes manifests absent from the snapshot, so it must
// see a complete VM+namespace view; but it never reads VMIs, so a permanently
// failing VMI reflector (e.g. a removed RBAC verb) must NOT wedge export. (Whole-
// snapshot readiness, incl. VMIs, is what WaitForSync's allSynced channel gates.)
func (s *State) ExportReady() bool {
	return s.vmsSynced.Load() && s.nssSynced.Load()
}

// VMObjects returns deep copies of the full VirtualMachine objects in the given
// namespaces — the exporter's input, replacing a per-tick SA LIST per project.
// Copies, because reflector-owned objects must never escape to be mutated.
func (s *State) VMObjects(namespaces []string) []kubevirtcorev1.VirtualMachine {
	want := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		want[ns] = true
	}
	var out []kubevirtcorev1.VirtualMachine
	for _, obj := range s.vms.List() {
		vm, ok := obj.(*kubevirtcorev1.VirtualMachine)
		if !ok || !want[vm.Namespace] {
			continue
		}
		out = append(out, *vm.DeepCopy())
	}
	return out
}

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
	s := vmi.Status
	live := LiveVM{
		Phase:    string(s.Phase),
		NodeName: s.NodeName,
		OS:       s.GuestOSInfo.PrettyName,
	}
	if len(s.Interfaces) > 0 {
		live.GuestIP = s.Interfaces[0].IP
	}
	for _, iface := range s.Interfaces {
		live.IPs = append(live.IPs, iface.IPs...)
		live.Interfaces = append(live.Interfaces, LiveNIC{Name: iface.Name, MAC: iface.MAC, IP: iface.IP})
	}
	if s.Memory != nil && s.Memory.GuestCurrent != nil {
		live.MemoryActual = s.Memory.GuestCurrent.String()
	}
	// Uptime is measured from when the VMI entered Running — not object creation,
	// which predates boot.
	for _, t := range s.PhaseTransitionTimestamps {
		if t.Phase == kubevirtcorev1.Running {
			live.StartedAt = t.PhaseTransitionTimestamp.Time
		}
	}
	for _, cond := range s.Conditions {
		switch cond.Type {
		case kubevirtcorev1.VirtualMachineInstanceReady:
			live.Ready = cond.Status == corev1.ConditionTrue
		case kubevirtcorev1.VirtualMachineInstancePaused:
			live.Paused = cond.Status == corev1.ConditionTrue
		}
	}
	if ms := s.MigrationState; ms != nil {
		m := &Migration{
			SourceNode: ms.SourceNode,
			TargetNode: ms.TargetNode,
			Completed:  ms.Completed,
			Failed:     ms.Failed,
		}
		if ms.StartTimestamp != nil {
			m.StartedAt = ms.StartTimestamp.Time
		}
		if ms.EndTimestamp != nil {
			m.EndedAt = ms.EndTimestamp.Time
		}
		live.Migration = m
	}
	return live
}
