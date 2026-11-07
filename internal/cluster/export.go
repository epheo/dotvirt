package cluster

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/vmgen"
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
	"restore.kubevirt.io/lastRestoreUID",    // restore/clone bookkeeping; churns on every restore
	"nmstate.io/webhook-mutating-timestamp", // stamped by nmstate's webhook on every NNCP update
}

// ExportManifest serializes a live VM into a clean, deterministic YAML manifest:
// apiVersion/kind restored, status dropped, and volatile metadata removed. The same
// cluster state always produces identical bytes, so an unchanged VM never reads as
// drift against the manifest git holds.
func ExportManifest(vm kubevirtcorev1.VirtualMachine) ([]byte, error) {
	return yaml.Marshal(exportedVM{
		TypeMeta: metav1.TypeMeta{APIVersion: kubevirtcorev1.SchemeGroupVersion.String(), Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        vm.Name,
			Namespace:   vm.Namespace,
			Labels:      vm.Labels,
			Annotations: stripAnnotations(vm.Annotations),
		},
		Spec: vm.Spec,
	})
}

// exportedVM is a VirtualMachine without its status, which has no omitempty
// and would otherwise marshal as "status: {}" into a desired-state manifest.
type exportedVM struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              kubevirtcorev1.VirtualMachineSpec `json:"spec"`
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

// ExportPath is the repo-relative path a serialized VM's manifest lands at when
// adopted: the wizard's layout, so adopt-then-edit converges on one file.
func ExportPath(vm kubevirtcorev1.VirtualMachine) string {
	return vmgen.ManifestPath(vm.Namespace, vm.Name)
}
