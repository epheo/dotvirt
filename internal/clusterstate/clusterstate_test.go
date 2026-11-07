package clusterstate

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/reflect"
)

// stateWithIndexers builds a State around fresh indexers the test can seed
// directly, standing in for what the reflectors would populate from the cluster.
func stateWithIndexers() *State {
	s := &State{vms: reflect.NewIndexer(), vmis: reflect.NewIndexer(), nss: reflect.NewIndexer()}
	s.live = newLiveIndex(s.vms, s.vmis, func() {})
	return s
}

// vmStore and vmiStore are the reflector sinks New wires, for tests that feed
// watch events.
func (s *State) vmStore() cache.Store  { return s.live.store(s.vms, nil) }
func (s *State) vmiStore() cache.Store { return s.live.store(s.vmis, nil) }

func runningVMI(ns, name, node string) *kubevirtcorev1.VirtualMachineInstance {
	return &kubevirtcorev1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status: kubevirtcorev1.VirtualMachineInstanceStatus{
			Phase:      kubevirtcorev1.Running,
			NodeName:   node,
			Interfaces: []kubevirtcorev1.VirtualMachineInstanceNetworkInterface{{IP: "10.0.0.5"}},
			Conditions: []kubevirtcorev1.VirtualMachineInstanceCondition{
				{Type: kubevirtcorev1.VirtualMachineInstanceReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

func testVM(ns, name string) *kubevirtcorev1.VirtualMachine {
	return &kubevirtcorev1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func TestLiveMergesVMIOntoVM(t *testing.T) {
	s := stateWithIndexers()
	// A stopped VM (no VMI) and a running VM (VM + VMI).
	_ = s.vmStore().Add(testVM("ns", "stopped"))
	_ = s.vmStore().Add(testVM("ns", "running"))
	_ = s.vmiStore().Add(runningVMI("ns", "running", "node-1"))

	live := s.Live()
	if got, ok := live["ns"]["stopped"]; !ok || got.Phase != "" {
		t.Errorf("stopped VM should be present with empty phase, got %+v (ok=%v)", got, ok)
	}
	r, ok := live["ns"]["running"]
	if !ok {
		t.Fatal("running VM missing from live state")
	}
	if r.Phase != "Running" || r.NodeName != "node-1" || r.GuestIP != "10.0.0.5" || !r.Ready {
		t.Errorf("running VM live state wrong: %+v", r)
	}
	if n := s.StoppedVMs([]string{"ns", "other"}); n != 1 {
		t.Errorf("StoppedVMs = %d, want 1", n)
	}
}

// LiveChanged wakes every connected frame, so it must fire only when what a frame
// shows moved: KubeVirt rewrites VM status and VMI status (probe times, guest
// agent polls) far more often than that. A view a frame already holds must
// never change underneath it.
func TestLivePublishesOnlyViewChanges(t *testing.T) {
	s := stateWithIndexers()
	var n int
	s.live.onChange = func() { n++ }
	vms, vmis := s.vmStore(), s.vmiStore()
	step := func(what string, want int) {
		t.Helper()
		if n != want {
			t.Fatalf("%s: %d publishes, want %d", what, n, want)
		}
	}

	_ = vms.Replace([]any{testVM("ns", "a")}, "1")
	step("initial list", 1)
	held := s.Live()

	vm := testVM("ns", "a")
	vm.Status.PrintableStatus = kubevirtcorev1.VirtualMachineStatusStopped
	_ = vms.Update(vm)
	step("VM status write", 1)

	_ = vmis.Add(runningVMI("ns", "a", "node-1"))
	step("VMI started", 2)
	if held["ns"]["a"].Phase != "" {
		t.Fatal("a held view was modified in place")
	}

	beat := runningVMI("ns", "a", "node-1")
	beat.Status.Conditions[0].LastProbeTime = metav1.Now()
	beat.Status.GuestOSInfo.Version = "40"
	_ = vmis.Update(beat)
	step("VMI heartbeat", 2)

	_ = vmis.Update(runningVMI("ns", "a", "node-2"))
	step("VMI migrated", 3)

	_ = vms.Add(testVM("ns", "b"))
	step("VM added", 4)

	_ = vmis.Delete(runningVMI("ns", "a", "node-2"))
	step("VMI stopped", 5)
	if got, ok := s.Live()["ns"]["a"]; !ok || got.Phase != "" {
		t.Fatalf("a VM whose VMI is gone must read stopped, got %+v ok=%v", got, ok)
	}

	_ = vms.Delete(testVM("ns", "a"))
	_ = vms.Delete(testVM("ns", "b"))
	step("VMs deleted", 7)
	if len(s.Live()) != 0 {
		t.Fatalf("an emptied namespace must leave the view, got %v", s.Live())
	}

	_ = vms.Replace(nil, "2")
	step("relist of nothing new", 7)
}

// WorkloadLabels prefers the running VMI's labels (what the virt-launcher pod
// carries) over the manifest template's, and falls back for stopped VMs.
func TestWorkloadLabels(t *testing.T) {
	s := stateWithIndexers()
	tmpl := &kubevirtcorev1.VirtualMachineInstanceTemplateSpec{}
	tmpl.ObjectMeta.Labels = map[string]string{"app": "authored"}
	_ = s.vms.Add(&kubevirtcorev1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "vm"},
		Spec:       kubevirtcorev1.VirtualMachineSpec{Template: tmpl},
	})

	lbls, live, found := s.WorkloadLabels("ns", "vm")
	if !found || live || lbls["app"] != "authored" {
		t.Errorf("stopped VM: want manifest labels, got %v live=%v found=%v", lbls, live, found)
	}

	_ = s.vmis.Add(&kubevirtcorev1.VirtualMachineInstance{ObjectMeta: metav1.ObjectMeta{
		Namespace: "ns", Name: "vm", Labels: map[string]string{"app": "live"},
	}})
	lbls, live, found = s.WorkloadLabels("ns", "vm")
	if !found || !live || lbls["app"] != "live" {
		t.Errorf("running VM: want VMI labels, got %v live=%v found=%v", lbls, live, found)
	}

	if _, _, found := s.WorkloadLabels("ns", "ghost"); found {
		t.Errorf("unknown VM must not be found")
	}
}

// WorkloadNetworks must mirror KubeVirt's VMI-creation defaulting for stopped
// VMs (a template naming no networks gets the pod network autoattached) and
// must report a default multus binding as a substituted segment, never as the
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
	podNet, defaultNet, _, _, found := s.WorkloadNetworks("ns", "minimal")
	if !found || !podNet || defaultNet != "" {
		t.Errorf("minimal template must autoattach the pod net: podNet=%v defaultNet=%q found=%v", podNet, defaultNet, found)
	}

	off := false
	noNet := tmpl()
	noNet.Spec.Domain.Devices.AutoattachPodInterface = &off
	addVM("unattached", noNet)
	if podNet, _, _, _, _ = s.WorkloadNetworks("ns", "unattached"); podNet {
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
	podNet, defaultNet, _, _, _ = s.WorkloadNetworks("ns", "substituted")
	if podNet || defaultNet != "ns/vlan" {
		t.Errorf("default multus binding: podNet=%v defaultNet=%q, want false ns/vlan", podNet, defaultNet)
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

// The store trims managedFields and the last-applied annotation on insert; the
// manifest drift and adoption serialize from the stored VM must not change.
func TestStoredVMExportsUnchanged(t *testing.T) {
	vm := testVM("ns", "web")
	vm.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationApply}}
	vm.Labels = map[string]string{"app": "web"}
	vm.Annotations = map[string]string{
		"kubectl.kubernetes.io/last-applied-configuration": `{"spec":{}}`,
		"keep": "yes",
	}
	rs := kubevirtcorev1.RunStrategyAlways
	vm.Spec.RunStrategy = &rs
	want, err := cluster.ExportManifest(*vm.DeepCopy())
	if err != nil {
		t.Fatal(err)
	}

	s := stateWithIndexers()
	_ = s.vmStore().Add(vm)
	objs := s.VMObjects([]string{"ns"})
	if len(objs) != 1 || objs[0].ManagedFields != nil {
		t.Fatalf("stored VM not trimmed: %+v", objs)
	}
	got, err := cluster.ExportManifest(objs[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("export moved:\n%s\nwant:\n%s", got, want)
	}
}
