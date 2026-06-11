package clusterstate

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
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

// TestCountingStoreBumpsAndSyncs checks the watch→signal plumbing: each mutation
// bumps version and fires on(); the first Replace (initial relist) additionally
// fires synced() exactly once.
func TestCountingStoreBumpsAndSyncs(t *testing.T) {
	var bumps, syncs int
	cs := &countingStore{
		Indexer: newIndexer(),
		on:      func() { bumps++ },
		synced:  func() { syncs++ },
	}

	obj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "a"}}
	_ = cs.Replace([]any{obj}, "1") // initial relist
	_ = cs.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "b"}})
	_ = cs.Replace([]any{obj}, "2") // a later relist must NOT re-fire synced

	if bumps != 3 {
		t.Errorf("every mutation should bump: want 3, got %d", bumps)
	}
	if syncs != 1 {
		t.Errorf("synced should fire exactly once (first Replace), got %d", syncs)
	}
}

var _ cache.Store = (*countingStore)(nil) // countingStore must satisfy the reflector's store
