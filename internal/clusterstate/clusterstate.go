// Package clusterstate maintains dotvirt's single source of live cluster truth.
//
// The read path used to fetch this per request, per subscriber: every inventory
// build issued a namespace LIST, a GET per namespace, and a VM+VMI LIST per
// namespace - all under the user's token, all repeated on every change. A handful
// of viewers plus a burst of VM events exhausted client-go's per-client rate
// limiter and wedged the server for minutes.
//
// Instead, reflectors run ONCE under dotvirt's ServiceAccount and keep in-memory
// indexers of VirtualMachines, VirtualMachineInstances, and the project-labeled
// Namespaces - the data is identical for every tenant, so it belongs in one shared
// snapshot, not in N per-user fetches. A read becomes a pure in-memory filter of
// this snapshot through the caller's visible-namespace set (computed elsewhere, per
// token): the cluster is never touched on the read path. This is the reflector
// model the argo drift snapshot also follows - fed by watches, not polled.
//
// Signal-only watches follow what a token's authorization derives from -
// RoleBindings and Roles in project namespaces, ClusterRoleBindings, the
// ClusterRoles those bind, and OpenShift Groups - and publish RBACChanged when
// a grant moves, so the per-token visibility and review caches are invalidated
// promptly instead of on their TTL. dotvirt never reads them (rbacSignal).
//
// The VM and VMI stores keep a derived live view current as they move and
// publish LiveChanged only when it changes; the namespace store publishes on any
// mutation. The hub subscribes to both; the read path diffs nothing.
//
// Authorization is unchanged: the snapshot is global truth, but a user only ever
// sees the namespaces their own RBAC grants - the filter is the security gate.
package clusterstate

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"log"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/cache"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/reflect"
)

// LiveVM is one VM's actual state, for merging into the git-derived inventory -
// clusterstate's own read DTO, derived from the watched VMI objects
// (inventory.applyLive copies it onto each model.VM).
type LiveVM struct {
	Phase    string
	GuestIP  string   // primary interface IP (the inventory grid's IP column)
	IPs      []string // every guest-reported IP, for the detail view
	NodeName string

	// Guest-agent + runtime facts for the VM summary dashboard. Empty when the VMI
	// isn't running or the guest agent isn't reporting.
	OS           string    // guest OS pretty name, e.g. "Fedora Linux 40 (Cloud Edition)"
	MemoryActual string    // current guest memory (hotplug-aware), e.g. "1Gi"
	VCPUs        int       // rendered vCPU topology; instancetype-sized VMs carry no cpuCores in git
	StartedAt    time.Time // when the VMI entered Running, for uptime

	Paused bool // VMI Paused condition is true (phase stays Running while paused)
	Ready  bool

	// Interfaces is each VMI interface's runtime address info, merged onto the
	// VM's manifest-declared NIC of the same name for the detail view's per-NIC
	// IP/MAC columns.
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

// Migration is a VM's live node-to-node move. Active
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
// (Live, Namespaces) are lock-free and safe for concurrent callers.
type State struct {
	vms  cache.Indexer
	vmis cache.Indexer
	nss  cache.Indexer
	live *liveIndex

	sa      *cluster.Client
	specs   []reflectorSpec // reflector wiring, built in New, started in Run
	healthy []atomic.Bool   // one reflect.TrackHealth flag per specs entry
	signals []*signalWatch  // RBAC watches gated on their grant, started in Run

	// The RoleBinding watch runs among specs; RBACWatched reads its sink and its
	// health flag (healthy[rbHealth]) beside the gated ones.
	rbSink   *rbacSignal
	rbHealth int

	grantMoved atomic.Int64 // unix nanos of the last RBACChanged the RBAC sinks published

	// Per-store readiness: each reflector's initial LIST (first Replace) landing.
	// Tracked per store (not a single counter) so a consumer gates on exactly the
	// stores it reads: drift must not stall on a failing VMI reflector, and
	// nobody waits on the RBAC signal watches.
	vmsReady, vmisReady, nssReady reflect.Ready

	bus *eventbus.Bus // reflectors publish their kind here on every mutation
}

// New builds the snapshot's reflectors over sa (dotvirt's ServiceAccount client),
// watching the namespaces labeled projectLabel. Each reflector publishes its kind to
// bus whenever the snapshot moves, so the inventory hub rebuilds; bus is optional
// (nil disables signalling, e.g. in tests).
func New(sa *cluster.Client, projectLabel string, bus *eventbus.Bus) *State {
	s := &State{bus: bus, sa: sa}
	s.vms = reflect.NewIndexer()
	s.vmis = reflect.NewIndexer()
	s.nss = reflect.NewIndexer()
	s.live = newLiveIndex(s.vms, s.vmis, s.publishLive)

	namespace := func() { bus.Publish(eventbus.NamespaceChanged) }
	rbac := func() {
		s.grantMoved.Store(time.Now().UnixNano())
		bus.Publish(eventbus.RBACChanged)
	}
	refs := newRoleRefs()
	s.specs = []reflectorSpec{
		{s.live.store(s.vms, s.vmsReady.Mark), &kubevirtcorev1.VirtualMachine{}, sa.VMListWatch()},
		{s.live.store(s.vmis, s.vmisReady.Mark), &kubevirtcorev1.VirtualMachineInstance{}, sa.VMIListWatch()},
		// A namespace move is both a topology change (inventory) and a visibility
		// change; NamespaceChanged is summed into both the inventory and RBAC versions.
		{reflect.NewStore(s.nss, namespace, s.nssReady.Mark), &corev1.Namespace{}, sa.NamespaceListWatch(projectLabel)},
	}
	// Every operand role grants RoleBindings, so their signal watch starts with
	// the snapshot; the other RBAC kinds wait for their grant (signals).
	s.rbSink, s.rbHealth = newRBACSignal(rbac, refs, roleBindingDigest(s.inProject)), len(s.specs)
	s.specs = append(s.specs, reflectorSpec{s.rbSink, &rbacv1.RoleBinding{}, sa.RoleBindingListWatch()})
	s.healthy = make([]atomic.Bool, len(s.specs))
	sources := map[schema.GroupVersionResource]*cache.ListWatch{
		gvrRoles:               sa.RoleListWatch(),
		gvrClusterRoleBindings: sa.ClusterRoleBindingListWatch(),
		gvrClusterRoles:        sa.ClusterRoleListWatch(),
		gvrGroups:              sa.DynamicListWatch(gvrGroups),
	}
	s.signals = s.signalWatches(func(gvr schema.GroupVersionResource) *cache.ListWatch { return sources[gvr] }, rbac, refs)
	return s
}

// The RBAC kinds watched once the SA may list them. Groups are OpenShift's
// (user.openshift.io), absent on other clusters.
var (
	gvrRoles               = rbacv1.SchemeGroupVersion.WithResource("roles")
	gvrClusterRoleBindings = rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings")
	gvrClusterRoles        = rbacv1.SchemeGroupVersion.WithResource("clusterroles")
	gvrGroups              = schema.GroupVersionResource{Group: "user.openshift.io", Version: "v1", Resource: "groups"}
)

// signalWatch is one gated RBAC signal watch: its source, its retain-nothing
// sink, and what RBACWatched reads of it.
type signalWatch struct {
	gvr      schema.GroupVersionResource
	expected any
	lw       *cache.ListWatch
	sink     *rbacSignal
	healthy  atomic.Bool // reflect.TrackHealth's, once admitted
	absent   atomic.Bool // the gate found the kind unserved: nothing to watch
}

// signalWatches wires the gated RBAC watches; refs is shared with the
// RoleBinding sink, so a ClusterRole signals while any relevant binding of
// either kind binds it.
func (s *State) signalWatches(lw func(schema.GroupVersionResource) *cache.ListWatch, onChange func(), refs *roleRefs) []*signalWatch {
	watch := func(gvr schema.GroupVersionResource, expected any, binds *roleRefs, digest func(any) (grant, bool)) *signalWatch {
		return &signalWatch{gvr: gvr, expected: expected, lw: lw(gvr), sink: newRBACSignal(onChange, binds, digest)}
	}
	return []*signalWatch{
		watch(gvrRoles, &rbacv1.Role{}, nil, roleDigest(s.inProject)),
		watch(gvrClusterRoleBindings, &rbacv1.ClusterRoleBinding{}, refs, clusterRoleBindingDigest),
		watch(gvrClusterRoles, &rbacv1.ClusterRole{}, nil, clusterRoleDigest(refs)),
		watch(gvrGroups, &unstructured.Unstructured{}, nil, groupDigest),
	}
}

func (s *State) inProject(ns string) bool {
	_, ok, _ := s.nss.GetByKey(ns)
	return ok
}

type reflectorSpec struct {
	store    cache.Store // already wrapped to fire the right Publishes + readiness
	expected any
	lw       *cache.ListWatch
}

// Run starts one reflector per resource; each owns its own relist/backoff and
// stops when ctx is cancelled. Returns immediately - call WaitForSync to block
// until the initial LIST has populated the snapshot.
func (s *State) Run(ctx context.Context) {
	for i, spec := range s.specs {
		reflect.RunTyped(ctx, reflect.TrackHealth(spec.lw, &s.healthy[i], s.publishLive), spec.expected, spec.store)
	}
	s.runSignals(ctx, listable(ctx, s.sa))
}

// runSignals starts each signal watch once gate admits its kind, re-asking on
// the discovery pace until it does. Their health stays out of Healthy: an
// outage only puts the authorization caches back on the short TTL
// (RBACWatched), while everything the snapshot serves stays current.
func (s *State) runSignals(ctx context.Context, gate func(schema.GroupVersionResource) admission) {
	for _, w := range s.signals {
		has := func(gvr schema.GroupVersionResource) bool {
			a := gate(gvr)
			w.absent.Store(a == absent)
			return a == admitted
		}
		reflect.RunTypedWhenServed(ctx, has, w.gvr, w.lw, w.expected, w.sink, &w.healthy, nil)
	}
}

// admission is a gate's answer for one RBAC kind.
type admission int

const (
	notYet   admission = iota // ask again later: no grant yet, or a review failed
	absent                    // granted, and this cluster does not serve the kind
	admitted                  // watch it
)

// listable admits a signal watch once the SA may list its kind (an operator
// release may not grant it yet) and the cluster serves it (Groups exist only
// on OpenShift). The review goes first: a cluster lacking the grant answers it
// without a discovery read. Until admitted it is asked once a minute; a
// missing grant is logged once, since until it lands the authorization caches
// stay on the short TTL.
func listable(ctx context.Context, sa *cluster.Client) func(schema.GroupVersionResource) admission {
	var warned sync.Map
	return func(gvr schema.GroupVersionResource) admission {
		ok, err := sa.Can(ctx, "list", gvr.Group, gvr.Resource, "")
		if err != nil {
			return notYet
		}
		if !ok {
			if _, dup := warned.LoadOrStore(gvr, true); !dup {
				log.Printf("clusterstate: may not list %s; authorization caches stay on the short TTL until it may", gvr.GroupResource())
			}
			return notYet
		}
		switch served, err := sa.ServesAPIResource(gvr); {
		case err != nil:
			return notYet
		case !served:
			return absent
		}
		return admitted
	}
}

// LastGrantMove is when an RBAC watch last reported a grant move, zero before
// any.
func (s *State) LastGrantMove() time.Time {
	if n := s.grantMoved.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

// RBACWatched reports whether every RBAC signal watch has listed and is
// watching now; a kind this cluster does not serve counts, there being nothing
// to miss. Only then does RBACChanged report each grant move, so only then may
// the per-token authorization caches keep an entry for minutes. A skewed
// operator role, a failed watch or one still listing keeps them on the short
// TTL: a missed revocation then waits seconds, not minutes.
func (s *State) RBACWatched() bool {
	if s.rbSink == nil || !s.rbSink.listed.Load() || !s.healthy[s.rbHealth].Load() {
		return false
	}
	for _, w := range s.signals {
		if !w.absent.Load() && (!w.sink.listed.Load() || !w.healthy.Load()) {
			return false
		}
	}
	return true
}

// publishLive also carries health flips: the frame's stale warning reads
// Healthy.
func (s *State) publishLive() { s.bus.Publish(eventbus.LiveChanged) }

// Healthy reports whether every watch is currently established
// (reflect.TrackHealth). The snapshot keeps serving its last-good stores while
// unhealthy; the inventory surfaces a "may be stale" warning so a sustained
// outage isn't silent.
func (s *State) Healthy() bool { return reflect.AllHealthy(s.healthy) }

// WaitForSync blocks until every READABLE reflector's initial LIST has landed (the
// VM, VMI and namespace stores - not the RBAC signal watches), or ctx is done.
// Returns ctx.Err() on cancellation, nil once synced.
func (s *State) WaitForSync(ctx context.Context) error {
	for _, r := range []*reflect.Ready{&s.vmsReady, &s.vmisReady, &s.nssReady} {
		if err := r.Wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

// VMSnapshotReady reports whether the VM and namespace stores have synced. Drift and
// adoption read absence as meaning: a VM missing from the snapshot reads as not running.
// They never read VMIs, so a permanently failing VMI reflector (e.g. a removed RBAC
// verb) must NOT wedge them. (Whole-snapshot readiness, incl. VMIs, is what
// WaitForSync gates.)
func (s *State) VMSnapshotReady() bool {
	return s.vmsReady.Done() && s.nssReady.Done()
}

// VMObjects returns deep copies of the full VirtualMachine objects in the given
// namespaces: what drift and adoption serialize, never a per-request SA LIST.
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

// Live returns the current live state of every VM in the snapshot - one view
// shared by all callers, which must not modify it. Pure in-memory, no copy.
func (s *State) Live() LiveView { return s.live.load() }

// StoppedVMs counts the VMs in namespaces that have no running instance.
func (s *State) StoppedVMs(namespaces []string) int {
	live := s.Live()
	n := 0
	for _, ns := range namespaces {
		for _, vm := range live[ns] {
			if vm.Phase == "" {
				n++
			}
		}
	}
	return n
}

// Workload is one VM as network policy sees it.
type Workload struct {
	// Labels are the virt-launcher pod's, the ones selectors match: the VMI's
	// own plus KubeVirt's fixed set.
	Labels map[string]string
	// Live: a running VMI answered. Otherwise the VM template supplied the
	// deterministic subset, without the instance UID or node a start assigns.
	Live bool

	// PodNet: binds the namespace's primary network. DefaultNet: the NAD a
	// default multus binding substitutes for it, "namespace/name"; it is NOT
	// the namespace primary. Nets: secondary multus refs, namespace-qualified.
	PodNet     bool
	DefaultNet string
	Nets       []string

	// IPs are the primary NIC's addresses, nil when stopped. Secondary and
	// guest-only interfaces never carry a flow the cluster policies filter.
	IPs []string
}

// Workload resolves one VM's network identity, from the running VMI (what
// actually runs) else the VM template; found=false when the VM is in neither
// store. Both Effective and Trace match against it. Copies - reflector-owned
// objects must never escape.
func (s *State) Workload(namespace, name string) (w Workload, found bool) {
	if obj, ok, _ := s.vmis.GetByKey(namespace + "/" + name); ok {
		if vmi, ok := obj.(*kubevirtcorev1.VirtualMachineInstance); ok {
			w.Live = true
			primary := w.attach(namespace, &vmi.Spec)
			w.Labels = launcherLabels(vmi.Name, vmi.Spec.Hostname, vmi.Labels, vmi.Annotations)
			w.Labels[kubevirtcorev1.CreatedByLabel] = string(vmi.UID)
			for _, iface := range vmi.Status.Interfaces {
				if primary == "" || iface.Name != primary {
					continue
				}
				if len(iface.IPs) > 0 {
					w.IPs = append(w.IPs, iface.IPs...)
				} else if iface.IP != "" {
					w.IPs = append(w.IPs, iface.IP)
				}
			}
			return w, true
		}
	}
	if obj, ok, _ := s.vms.GetByKey(namespace + "/" + name); ok {
		if vm, ok := obj.(*kubevirtcorev1.VirtualMachine); ok {
			t := vm.Spec.Template
			if t == nil {
				t = &kubevirtcorev1.VirtualMachineInstanceTemplateSpec{}
			}
			w.attach(namespace, &t.Spec)
			// KubeVirt autoattaches the pod network when a template names
			// none; that defaulting lands on the VMI at creation, never on the
			// stored template, so mirror it here or a stopped minimal manifest
			// reads as unattached.
			if len(t.Spec.Networks) == 0 {
				if auto := t.Spec.Domain.Devices.AutoattachPodInterface; auto == nil || *auto {
					w.PodNet = true
				}
			}
			// The VMI a start creates takes the template's metadata and the VM's name.
			w.Labels = launcherLabels(vm.Name, t.Spec.Hostname, t.ObjectMeta.Labels, t.ObjectMeta.Annotations)
			return w, true
		}
	}
	return Workload{}, false
}

// attach records spec's NIC bindings and returns the primary one's network
// name (pod or default multus), "" when there is none.
func (w *Workload) attach(namespace string, spec *kubevirtcorev1.VirtualMachineInstanceSpec) (primary string) {
	qualify := func(ref string) string {
		if strings.Contains(ref, "/") {
			return ref
		}
		return namespace + "/" + ref
	}
	for _, n := range spec.Networks {
		switch {
		case n.Pod != nil:
			w.PodNet, primary = true, n.Name
		case n.Multus != nil && n.Multus.Default:
			w.DefaultNet, primary = qualify(n.Multus.NetworkName), n.Name
		case n.Multus != nil:
			w.Nets = append(w.Nets, qualify(n.Multus.NetworkName))
		}
	}
	return primary
}

// istioInject is copied from a VMI annotation to the same pod label.
const istioInject = "sidecar.istio.io/inject"

// launcherLabels mirrors virt-controller's virt-launcher pod labels (KubeVirt
// v1.8 services.podLabels) except kubevirt.io/created-by, the VMI UID, which
// only a live VMI has.
func launcherLabels(name, hostname string, labels, annotations map[string]string) map[string]string {
	out := maps.Clone(labels)
	if out == nil {
		out = map[string]string{}
	}
	out[kubevirtcorev1.AppLabel] = "virt-launcher"
	out[kubevirtcorev1.DeprecatedVirtualMachineNameLabel] = launcherHostname(name, hostname)
	out[kubevirtcorev1.VirtualMachineInstanceIDLabel] = vmiID(name)
	if v, ok := annotations[istioInject]; ok {
		out[istioInject] = v
	}
	return out
}

// launcherHostname is KubeVirt's dns.SanitizeHostname.
func launcherHostname(name, hostname string) string {
	if hostname != "" {
		return hostname
	}
	h, _, _ := strings.Cut(name, ".")
	if len(h) > validation.DNS1123LabelMaxLength {
		h = h[:validation.DNS1123LabelMaxLength]
	}
	return h
}

// vmiID is KubeVirt's apimachinery.CalculateVirtualMachineInstanceID: the
// name, or a hash-suffixed prefix when it overflows a label value.
func vmiID(name string) string {
	if len(name) <= validation.DNS1035LabelMaxLength {
		return name
	}
	const hashLen = 8
	sum := sha1.Sum([]byte(name)) // KubeVirt's derivation, not a security use
	return name[:validation.DNS1035LabelMaxLength-hashLen-1] + "-" + hex.EncodeToString(sum[:])[:hashLen]
}

// Holds reports whether the snapshot holds the object of resource (group,
// plural) named namespace/name: a VM, or a project namespace, the only
// namespaces it watches. A kind it does not watch reads as absent.
func (s *State) Holds(group, resource, namespace, name string) bool {
	var ok bool
	switch {
	case group == model.KindVM.Group && resource == model.KindVM.Plural:
		_, ok, _ = s.vms.GetByKey(namespace + "/" + name)
	case group == model.KindNS.Group && resource == model.KindNS.Plural:
		_, ok, _ = s.nss.GetByKey(name)
	}
	return ok
}

// NamespaceLabels returns one namespace's labels by name, nil when the
// snapshot does not hold it - an indexer key read, not a scan of Namespaces.
func (s *State) NamespaceLabels(name string) map[string]string {
	if obj, ok, _ := s.nss.GetByKey(name); ok {
		if ns, ok := obj.(*corev1.Namespace); ok {
			return maps.Clone(ns.Labels)
		}
	}
	return nil
}

// Namespaces returns the project-labeled namespaces in the snapshot as the
// resolver's input type (Name + labels/annotations), so the read path feeds
// project.Resolve directly. Pure in-memory, no cluster call.
func (s *State) Namespaces() []project.Namespace {
	objs := s.nss.List()
	out := make([]project.Namespace, 0, len(objs))
	for _, obj := range objs {
		ns, ok := obj.(*corev1.Namespace)
		if !ok {
			continue
		}
		out = append(out, project.Namespace{Name: ns.Name, Labels: ns.Labels, Annotations: ns.Annotations, Created: ns.CreationTimestamp.Time})
	}
	return out
}

// cpuProduct is sockets*cores*threads with KubeVirt's zero-means-one default.
func cpuProduct(sockets, cores, threads uint32) int {
	n := func(v uint32) int {
		if v == 0 {
			return 1
		}
		return int(v)
	}
	return n(sockets) * n(cores) * n(threads)
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
	// The rendered topology, hotplug-aware when status reports one.
	if topo := s.CurrentCPUTopology; topo != nil {
		live.VCPUs = cpuProduct(topo.Sockets, topo.Cores, topo.Threads)
	} else if cpu := vmi.Spec.Domain.CPU; cpu != nil {
		live.VCPUs = cpuProduct(cpu.Sockets, cpu.Cores, cpu.Threads)
	}
	// Uptime is measured from when the VMI entered Running - not object creation,
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
