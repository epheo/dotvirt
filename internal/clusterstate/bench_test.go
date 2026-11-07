package clusterstate

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtcorev1 "kubevirt.io/api/core/v1"
)

// benchVMI is a running VMI with the status fields liveFromVMI reads.
func benchVMI(ns, name string) *kubevirtcorev1.VirtualMachineInstance {
	return &kubevirtcorev1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status: kubevirtcorev1.VirtualMachineInstanceStatus{
			Phase:    kubevirtcorev1.Running,
			NodeName: "worker-1",
			Interfaces: []kubevirtcorev1.VirtualMachineInstanceNetworkInterface{
				{Name: "default", IP: "10.0.0.5", IPs: []string{"10.0.0.5", "fd00::5"}, MAC: "02:00:00:00:00:05"},
			},
			Conditions: []kubevirtcorev1.VirtualMachineInstanceCondition{
				{Type: kubevirtcorev1.VirtualMachineInstanceReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

// BenchmarkLiveVMs is the per-frame live read at 2000 running VMs in 50
// namespaces.
func BenchmarkLiveVMs(b *testing.B) {
	s := stateWithIndexers()
	for i := range 2000 {
		ns, name := fmt.Sprintf("ns-%d", i%50), fmt.Sprintf("vm-%d", i)
		_ = s.vms.Add(&kubevirtcorev1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}})
		_ = s.vmis.Add(benchVMI(ns, name))
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = s.LiveVMs()
	}
}
