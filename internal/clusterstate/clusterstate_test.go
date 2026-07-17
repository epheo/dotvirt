package clusterstate

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtcorev1 "kubevirt.io/api/core/v1"
)

// stateWithIndexers builds a State around fresh indexers the test can seed
// directly, standing in for what the reflectors would populate from the cluster.
func stateWithIndexers() *State {
	return &State{vms: newIndexer(), vmis: newIndexer(), nss: newIndexer()}
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
}

func testVM(gen int64) *kubevirtcorev1.VirtualMachine {
	return &kubevirtcorev1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "vm", Generation: gen}}
}

// TestVMSpecStoreGatesOnGeneration is the core of the export-CPU fix: a status-only
// VM Update (generation unchanged) must fire LiveChanged but NOT VMSpecChanged, so
// the exporter doesn't wake on KubeVirt's frequent VM.status writes. Add/Delete and a
// real spec change (generation bump) fire both.
func TestVMSpecStoreGatesOnGeneration(t *testing.T) {
	var spec, live int
	store := newVMSpecStore(newIndexer(), func() { spec++ }, func() { live++ }, nil)

	if err := store.Add(testVM(1)); err != nil {
		t.Fatal(err)
	}
	if spec != 1 || live != 1 {
		t.Fatalf("after Add: spec=%d live=%d, want 1,1", spec, live)
	}
	// Status-only Update (same generation) → LiveChanged only.
	_ = store.Update(testVM(1))
	if spec != 1 || live != 2 {
		t.Errorf("after status-only Update: spec=%d live=%d, want 1,2", spec, live)
	}
	// Spec change (generation bump) → both.
	_ = store.Update(testVM(2))
	if spec != 2 || live != 3 {
		t.Errorf("after spec Update: spec=%d live=%d, want 2,3", spec, live)
	}
	// Delete → both (prunes the manifest).
	_ = store.Delete(testVM(2))
	if spec != 3 || live != 4 {
		t.Errorf("after Delete: spec=%d live=%d, want 3,4", spec, live)
	}
}

func TestVMSpecStoreReplaceFiresSyncedOnce(t *testing.T) {
	var spec, live, synced int
	store := newVMSpecStore(newIndexer(), func() { spec++ }, func() { live++ }, func() { synced++ })
	_ = store.Replace([]any{testVM(1)}, "1")
	_ = store.Replace([]any{testVM(1)}, "2") // a later relist must NOT re-fire synced
	if spec != 2 || live != 2 {
		t.Errorf("two Replaces: spec=%d live=%d, want 2,2", spec, live)
	}
	if synced != 1 {
		t.Errorf("synced fired %d times, want exactly 1 (first Replace)", synced)
	}
}
