package cluster

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtcorev1 "kubevirt.io/api/core/v1"
)

func sampleVM() kubevirtcorev1.VirtualMachine {
	vm := kubevirtcorev1.VirtualMachine{}
	vm.Name = "web"
	vm.Namespace = "alpha"
	vm.UID = "should-be-stripped"
	vm.ResourceVersion = "12345"
	vm.Labels = map[string]string{"app": "web"}
	vm.Annotations = map[string]string{
		"kubectl.kubernetes.io/last-applied-configuration": "{...}",
		"argocd.argoproj.io/tracking-id":                   "x",
		"keep-me":                                          "yes",
	}
	rs := kubevirtcorev1.RunStrategyAlways
	vm.Spec.RunStrategy = &rs
	return vm
}

func TestExportManifestDeterministicAndClean(t *testing.T) {
	vm := sampleVM()

	a, err := ExportManifest(vm)
	if err != nil {
		t.Fatalf("ExportManifest: %v", err)
	}
	b, err := ExportManifest(vm)
	if err != nil {
		t.Fatalf("ExportManifest: %v", err)
	}
	if string(a) != string(b) {
		t.Fatal("export not deterministic: same VM produced different bytes")
	}

	s := string(a)
	for _, banned := range []string{"resourceVersion", "managedFields", "status:", "last-applied-configuration", "tracking-id"} {
		if strings.Contains(s, banned) {
			t.Errorf("export leaked %q:\n%s", banned, s)
		}
	}
	if !strings.Contains(s, "keep-me") {
		t.Error("export dropped a non-volatile annotation it should keep")
	}
	if !strings.Contains(s, "runStrategy: Always") {
		t.Error("export lost runStrategy")
	}
}

func TestExportPath(t *testing.T) {
	vm := kubevirtcorev1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "beta"}}
	if got := ExportPath(vm); got != "beta/db.yaml" {
		t.Errorf("ExportPath = %q, want beta/db.yaml", got)
	}
}
