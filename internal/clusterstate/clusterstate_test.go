package clusterstate

import (
	"maps"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/reflect"
)

// stateWithIndexers builds a State around fresh indexers the test can seed
// directly, standing in for what the reflectors would populate from the cluster.
func stateWithIndexers() *State {
	return &State{vms: reflect.NewIndexer(), vmis: reflect.NewIndexer(), nss: reflect.NewIndexer()}
}

func TestLiveVMsMergesVMIOntoVM(t *testing.T) {
	s := stateWithIndexers()
	// A stopped VM (no VMI) and a running VM (VM + VMI).
	_ = s.vms.Add(&kubevirtcorev1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "stopped"}})
	_ = s.vms.Add(&kubevirtcorev1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "running"}})
	_ = s.vmis.Add(&kubevirtcorev1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "running"},
		Status: kubevirtcorev1.VirtualMachineInstanceStatus{
			Phase:      kubevirtcorev1.Running,
			NodeName:   "node-1",
			Interfaces: []kubevirtcorev1.VirtualMachineInstanceNetworkInterface{{IP: "10.0.0.5"}},
			Conditions: []kubevirtcorev1.VirtualMachineInstanceCondition{
				{Type: kubevirtcorev1.VirtualMachineInstanceReady, Status: corev1.ConditionTrue},
			},
		},
	})

	live := s.LiveVMs()
	if got, ok := live["ns/stopped"]; !ok || got.Phase != "" {
		t.Errorf("stopped VM should be present with empty phase, got %+v (ok=%v)", got, ok)
	}
	r, ok := live["ns/running"]
	if !ok {
		t.Fatal("running VM missing from live state")
	}
	if r.Phase != "Running" || r.NodeName != "node-1" || r.GuestIP != "10.0.0.5" || !r.Ready {
		t.Errorf("running VM live state wrong: %+v", r)
	}
}

// Selectors match the virt-launcher pod, not the VMI: the identity carries
// KubeVirt's fixed launcher labels on top of the VMI's own (the template's
// for a stopped VM, minus the instance UID a start assigns).
func TestWorkloadLabels(t *testing.T) {
	s := stateWithIndexers()
	tmpl := &kubevirtcorev1.VirtualMachineInstanceTemplateSpec{}
	tmpl.ObjectMeta.Labels = map[string]string{"app": "authored"}
	tmpl.ObjectMeta.Annotations = map[string]string{"sidecar.istio.io/inject": "true"}
	_ = s.vms.Add(&kubevirtcorev1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "vm.example"},
		Spec:       kubevirtcorev1.VirtualMachineSpec{Template: tmpl},
	})

	w, found := s.Workload("ns", "vm.example")
	want := map[string]string{
		"app":                     "authored",
		"kubevirt.io":             "virt-launcher",
		"vm.kubevirt.io/name":     "vm",
		"vmi.kubevirt.io/id":      "vm.example",
		"sidecar.istio.io/inject": "true",
	}
	if !found || w.Live || !maps.Equal(w.Labels, want) {
		t.Errorf("stopped VM: labels %v live=%v found=%v, want %v", w.Labels, w.Live, found, want)
	}
	if tmpl.ObjectMeta.Labels["kubevirt.io"] != "" {
		t.Error("the template's labels must not be mutated")
	}

	_ = s.vmis.Add(&kubevirtcorev1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "vm.example", UID: "u-1", Labels: map[string]string{"app": "live"}},
		Spec:       kubevirtcorev1.VirtualMachineInstanceSpec{Hostname: "web"},
	})
	w, found = s.Workload("ns", "vm.example")
	want = map[string]string{
		"app":                    "live",
		"kubevirt.io":            "virt-launcher",
		"kubevirt.io/created-by": "u-1",
		"vm.kubevirt.io/name":    "web",
		"vmi.kubevirt.io/id":     "vm.example",
	}
	if !found || !w.Live || !maps.Equal(w.Labels, want) {
		t.Errorf("running VM: labels %v live=%v found=%v, want %v", w.Labels, w.Live, found, want)
	}

	if _, found := s.Workload("ns", "ghost"); found {
		t.Errorf("unknown VM must not be found")
	}
}

// A VMI name past a label value's length gets KubeVirt's hashed id.
func TestVMIIDLongName(t *testing.T) {
	name := strings.Repeat("a", 70)
	if got, want := vmiID(name), strings.Repeat("a", 54)+"-"+"ed6c69d9"; got != want {
		t.Errorf("vmiID = %q, want %q", got, want)
	}
	if got := vmiID("short"); got != "short" {
		t.Errorf("vmiID(short) = %q", got)
	}
}

// Workload must mirror KubeVirt's VMI-creation defaulting for stopped VMs (a
// template naming no networks gets the pod network autoattached) and must
// report a default multus binding as a substituted segment, never as the
// namespace primary.
func TestWorkloadNetworks(t *testing.T) {
	s := stateWithIndexers()
	tmpl := func() *kubevirtcorev1.VirtualMachineInstanceTemplateSpec {
		return &kubevirtcorev1.VirtualMachineInstanceTemplateSpec{}
	}
	addVM := func(name string, t *kubevirtcorev1.VirtualMachineInstanceTemplateSpec) {
		_ = s.vms.Add(&kubevirtcorev1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name},
			Spec:       kubevirtcorev1.VirtualMachineSpec{Template: t},
		})
	}

	addVM("minimal", tmpl())
	w, found := s.Workload("ns", "minimal")
	if !found || !w.PodNet || w.DefaultNet != "" {
		t.Errorf("minimal template must autoattach the pod net: %+v found=%v", w, found)
	}

	off := false
	noNet := tmpl()
	noNet.Spec.Domain.Devices.AutoattachPodInterface = &off
	addVM("unattached", noNet)
	if w, _ = s.Workload("ns", "unattached"); w.PodNet {
		t.Error("autoattach off must not report a pod net")
	}

	sub := tmpl()
	sub.Spec.Networks = []kubevirtcorev1.Network{{
		Name: "nic0",
		NetworkSource: kubevirtcorev1.NetworkSource{
			Multus: &kubevirtcorev1.MultusNetwork{NetworkName: "vlan", Default: true},
		},
	}}
	addVM("substituted", sub)
	if w, _ = s.Workload("ns", "substituted"); w.PodNet || w.DefaultNet != "ns/vlan" {
		t.Errorf("default multus binding: podNet=%v defaultNet=%q, want false ns/vlan", w.PodNet, w.DefaultNet)
	}
}

// Only the primary NIC carries the flows the cluster policies filter: a
// secondary NIC's or a guest-only interface's address must never reach a
// CIDR peer match.
func TestWorkloadIPsPrimaryOnly(t *testing.T) {
	s := stateWithIndexers()
	_ = s.vmis.Add(&kubevirtcorev1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "vm"},
		Spec: kubevirtcorev1.VirtualMachineInstanceSpec{Networks: []kubevirtcorev1.Network{
			{Name: "default", NetworkSource: kubevirtcorev1.NetworkSource{Pod: &kubevirtcorev1.PodNetwork{}}},
			{Name: "dc", NetworkSource: kubevirtcorev1.NetworkSource{Multus: &kubevirtcorev1.MultusNetwork{NetworkName: "vlan10"}}},
		}},
		Status: kubevirtcorev1.VirtualMachineInstanceStatus{Interfaces: []kubevirtcorev1.VirtualMachineInstanceNetworkInterface{
			{Name: "default", IP: "10.128.2.5", IPs: []string{"10.128.2.5", "fd02::5"}},
			{Name: "dc", IP: "192.168.10.5", IPs: []string{"192.168.10.5"}},
			{InterfaceName: "docker0", IP: "172.17.0.1", IPs: []string{"172.17.0.1"}},
		}},
	})
	w, _ := s.Workload("ns", "vm")
	if !slices.Equal(w.IPs, []string{"10.128.2.5", "fd02::5"}) {
		t.Errorf("IPs = %v, want the primary NIC's only", w.IPs)
	}
	if !slices.Equal(w.Nets, []string{"ns/vlan10"}) {
		t.Errorf("Nets = %v", w.Nets)
	}
}

func TestNamespacesExposesLabelsAndAnnotations(t *testing.T) {
	s := stateWithIndexers()
	_ = s.nss.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        "tenant-a",
		Labels:      map[string]string{"dotvirt.io/project": "team-a"},
		Annotations: map[string]string{"dotvirt.io/repo": "https://forge/team-a.git"},
	}})

	got := s.Namespaces() // returns []project.Namespace
	if len(got) != 1 || got[0].Name != "tenant-a" {
		t.Fatalf("want one namespace tenant-a, got %+v", got)
	}
	if got[0].Labels["dotvirt.io/project"] != "team-a" || got[0].Annotations["dotvirt.io/repo"] != "https://forge/team-a.git" {
		t.Errorf("namespace labels/annotations not exposed: %+v", got[0])
	}
	if lbls := s.NamespaceLabels("tenant-a"); lbls["dotvirt.io/project"] != "team-a" {
		t.Errorf("NamespaceLabels(tenant-a) = %v", lbls)
	}
	if lbls := s.NamespaceLabels("absent"); lbls != nil {
		t.Errorf("NamespaceLabels(absent) = %v, want nil", lbls)
	}
}

func testVM(gen int64) *kubevirtcorev1.VirtualMachine {
	return &kubevirtcorev1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "vm", Generation: gen}}
}

// TestVMSpecStoreGatesOnGeneration is the core of the export-CPU fix: a status-only
// VM Update (generation unchanged) must fire LiveChanged but NOT VMSpecChanged, so
// VMSpecChanged consumers don't wake on KubeVirt's frequent VM.status writes. Add/Delete and a
// real spec change (generation bump) fire both.
func TestVMSpecStoreGatesOnGeneration(t *testing.T) {
	var spec, live int
	store := newVMSpecStore(reflect.NewIndexer(), func() { spec++ }, func() { live++ }, nil)

	if err := store.Add(testVM(1)); err != nil {
		t.Fatal(err)
	}
	if spec != 1 || live != 1 {
		t.Fatalf("after Add: spec=%d live=%d, want 1,1", spec, live)
	}
	// Status-only Update (same generation) -> LiveChanged only.
	_ = store.Update(testVM(1))
	if spec != 1 || live != 2 {
		t.Errorf("after status-only Update: spec=%d live=%d, want 1,2", spec, live)
	}
	// Spec change (generation bump) -> both.
	_ = store.Update(testVM(2))
	if spec != 2 || live != 3 {
		t.Errorf("after spec Update: spec=%d live=%d, want 2,3", spec, live)
	}
	// Delete -> both (prunes the manifest).
	_ = store.Delete(testVM(2))
	if spec != 3 || live != 4 {
		t.Errorf("after Delete: spec=%d live=%d, want 3,4", spec, live)
	}
}

func TestVMSpecStoreReplaceFiresSyncedOnce(t *testing.T) {
	var spec, live, synced int
	store := newVMSpecStore(reflect.NewIndexer(), func() { spec++ }, func() { live++ }, func() { synced++ })
	_ = store.Replace([]any{testVM(1)}, "1")
	_ = store.Replace([]any{testVM(1)}, "2") // a later relist must NOT re-fire synced
	if spec != 2 || live != 2 {
		t.Errorf("two Replaces: spec=%d live=%d, want 2,2", spec, live)
	}
	if synced != 1 {
		t.Errorf("synced fired %d times, want exactly 1 (first Replace)", synced)
	}
}
