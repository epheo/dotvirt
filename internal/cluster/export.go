package cluster

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	kubevirtcorev1 "kubevirt.io/api/core/v1"
)

// annotationsToStrip are server- or controller-set annotations that would make
// exports non-deterministic (they change without the VM's desired state
// changing). Mirrors the kinds of fields ArgoCD itself ignores.
var annotationsToStrip = []string{
	"kubectl.kubernetes.io/last-applied-configuration",
	"kubevirt.io/latest-observed-api-version",
	"kubevirt.io/storage-observed-api-version",
	"kubemacpool.io/transaction-timestamp",
	"argocd.argoproj.io/tracking-id",
}

// ExportManifest serializes a live VM into a clean, deterministic YAML manifest:
// apiVersion/kind restored, status dropped, and volatile metadata removed. The
// same cluster state always produces identical bytes, so re-exports don't churn
// the running branch.
func ExportManifest(vm kubevirtcorev1.VirtualMachine) ([]byte, error) {
	clean := kubevirtcorev1.VirtualMachine{
		Spec: vm.Spec,
	}
	clean.APIVersion = kubevirtcorev1.SchemeGroupVersion.String()
	clean.Kind = "VirtualMachine"
	clean.ObjectMeta = metav1.ObjectMeta{
		Name:        vm.Name,
		Namespace:   vm.Namespace,
		Labels:      vm.Labels,
		Annotations: stripAnnotations(vm.Annotations),
	}
	// Status is desired-state-irrelevant; leave it zero so it serializes minimally.
	clean.Status = kubevirtcorev1.VirtualMachineStatus{}

	raw, err := yaml.Marshal(clean)
	if err != nil {
		return nil, err
	}
	// VirtualMachineStatus has no omitempty, so it marshals as "status: {}".
	// Drop it: a desired-state manifest shouldn't carry an empty status block.
	return dropEmptyStatus(raw)
}

// dropEmptyStatus round-trips through a generic map to remove a top-level empty
// "status" key, independent of which status subfields happen to be zero.
func dropEmptyStatus(raw []byte) ([]byte, error) {
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if s, ok := m["status"]; ok {
		if sm, ok := s.(map[string]any); ok && len(sm) == 0 {
			delete(m, "status")
		}
	}
	return yaml.Marshal(m)
}

func stripAnnotations(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	for _, k := range annotationsToStrip {
		delete(out, k)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ExportPath is the repo-relative path a VM's manifest is written to on the
// running branch: one file per VM, grouped by namespace directory.
func ExportPath(vm kubevirtcorev1.VirtualMachine) string {
	return vm.Namespace + "/" + vm.Name + ".yaml"
}
