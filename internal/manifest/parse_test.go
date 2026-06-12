package manifest

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

func TestParseVMs(t *testing.T) {
	manifest := []byte(`
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
spec:
  runStrategy: Always
  template:
    spec:
      domain:
        cpu:
          cores: 2
        memory:
          guest: 2Gi
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ignore
---
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: legacy
spec:
  running: false
  template:
    spec:
      domain:
        cpu:
          cores: 1
        resources:
          requests:
            memory: 512Mi
`)

	vms, err := ParseVMs("alpha/x.yaml", manifest, "fallback-ns")
	if err != nil {
		t.Fatalf("ParseVMs: %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("want 2 VMs (ConfigMap ignored), got %d", len(vms))
	}

	web := vms[0]
	if web.Name != "web" || web.Namespace != "alpha" || web.Power != model.PowerOn ||
		web.CPUCores != 2 || web.Memory != "2Gi" {
		t.Errorf("web parsed wrong: %+v", web)
	}

	legacy := vms[1]
	if legacy.Name != "legacy" || legacy.Namespace != "fallback-ns" ||
		legacy.Power != model.PowerOff || legacy.Memory != "512Mi" {
		t.Errorf("legacy parsed wrong (running:false + fallback ns + legacy memory): %+v", legacy)
	}
}

func TestPowerFromRunStrategy(t *testing.T) {
	cases := map[string]model.Power{
		"Always":         model.PowerOn,
		"RerunOnFailure": model.PowerOn,
		"Halted":         model.PowerOff,
		"Manual":         model.PowerUnknown,
		"":               model.PowerUnknown,
	}
	for rs, want := range cases {
		var d vmDoc
		d.Spec.RunStrategy = rs
		if got := powerFromDoc(d); got != want {
			t.Errorf("runStrategy %q: want %v, got %v", rs, want, got)
		}
	}
}

// Disks join their volumes for type, and dataVolume volumes join their
// dataVolumeTemplates for provisioned size + storage class (both the storage
// and the legacy pvc spec forms).
func TestParseVMsDecodesDataVolumeStorage(t *testing.T) {
	manifest := []byte(`
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: db
  namespace: alpha
spec:
  runStrategy: Always
  dataVolumeTemplates:
    - metadata:
        name: db-rootdisk
      spec:
        sourceRef:
          kind: DataSource
          name: fedora
        storage:
          storageClassName: lvms-vgfast
          resources:
            requests:
              storage: 30Gi
    - metadata:
        name: db-data
      spec:
        pvc:
          storageClassName: lvms-vgslow
          resources:
            requests:
              storage: 100Gi
  template:
    spec:
      domain:
        devices:
          disks:
            - name: rootdisk
            - name: data
            - name: scratch
      volumes:
        - name: rootdisk
          dataVolume:
            name: db-rootdisk
        - name: data
          dataVolume:
            name: db-data
        - name: scratch
          emptyDisk:
            capacity: 2Gi
`)
	vms, err := ParseVMs("alpha/db.yaml", manifest, "alpha")
	if err != nil {
		t.Fatalf("ParseVMs: %v", err)
	}
	disks := vms[0].Disks
	if len(disks) != 3 {
		t.Fatalf("want 3 disks, got %+v", disks)
	}
	root := disks[0]
	if root.Type != "dataVolume" || root.Size != "30Gi" || root.StorageClass != "lvms-vgfast" {
		t.Errorf("rootdisk should carry its DV template's size+class: %+v", root)
	}
	data := disks[1]
	if data.Type != "dataVolume" || data.Size != "100Gi" || data.StorageClass != "lvms-vgslow" {
		t.Errorf("data disk should decode the legacy pvc spec form: %+v", data)
	}
	scratch := disks[2]
	if scratch.Type != "emptyDisk" || scratch.Size != "2Gi" || scratch.StorageClass != "" {
		t.Errorf("emptyDisk unchanged by DV join: %+v", scratch)
	}
}
