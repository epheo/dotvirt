package cluster

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtcorev1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/yaml"
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

// Drift compares these bytes against git, so the serialization itself is pinned:
// keys sorted, quoted scalars that would retype, block strings kept, no status.
func TestExportManifestBytes(t *testing.T) {
	var vm kubevirtcorev1.VirtualMachine
	if err := yaml.Unmarshal([]byte(`apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
  labels:
    version: "1.0"
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: '{}'
    note: "line1\nline2"
spec:
  runStrategy: Always
  template:
    spec:
      terminationGracePeriodSeconds: 180
      domain:
        cpu:
          cores: 2
        memory:
          guest: 1Gi
        devices: {}
      volumes:
      - name: cloudinitdisk
        cloudInitNoCloud:
          userData: |
            #cloud-config
            password: "1234"
status:
  printableStatus: Running
`), &vm); err != nil {
		t.Fatal(err)
	}
	want := `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  annotations:
    note: |-
      line1
      line2
  labels:
    version: "1.0"
  name: web
  namespace: alpha
spec:
  runStrategy: Always
  template:
    metadata: {}
    spec:
      domain:
        cpu:
          cores: 2
        devices: {}
        memory:
          guest: 1Gi
        resources: {}
      terminationGracePeriodSeconds: 180
      volumes:
      - cloudInitNoCloud:
          userData: |
            #cloud-config
            password: "1234"
        name: cloudinitdisk
`
	got, err := ExportManifest(vm)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("export bytes moved:\n%s\nwant:\n%s", got, want)
	}
}

func TestExportPath(t *testing.T) {
	vm := kubevirtcorev1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "beta"}}
	if got := ExportPath(vm); got != "beta/db.yaml" {
		t.Errorf("ExportPath = %q, want beta/db.yaml", got)
	}
}
