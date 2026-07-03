package manifest

import (
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// vmWithDataVolumes mirrors a wizard-generated VM: two disks backed by
// dataVolumeTemplates — the root disk imported from a registry image on an
// explicit class, the data disk blank on the cluster default.
const vmWithDataVolumes = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
spec:
  runStrategy: Always
  dataVolumeTemplates:
  - metadata:
      name: web-rootdisk
    spec:
      source:
        registry:
          url: docker://quay.io/containerdisks/fedora:41
      storage:
        storageClassName: standard
        resources:
          requests:
            storage: 30Gi
  - metadata:
      name: web-data
    spec:
      source:
        blank: {}
      storage:
        resources:
          requests:
            storage: 20Gi
  template:
    spec:
      domain:
        devices:
          disks:
          - name: rootdisk
            disk:
              bus: virtio
          - name: data
            disk:
              bus: virtio
      volumes:
      - name: rootdisk
        dataVolume:
          name: web-rootdisk
      - name: data
        dataVolume:
          name: web-data
      - name: cloudinit
        cloudInitNoCloud:
          userData: '#cloud-config'
`

func specOf(m map[string]any) map[string]any {
	return m["spec"].(map[string]any)
}

func dvTemplates(m map[string]any) []any {
	return specOf(m)["dataVolumeTemplates"].([]any)
}

func dvTemplate(m map[string]any, i int) (name string, spec map[string]any) {
	t := dvTemplates(m)[i].(map[string]any)
	return t["metadata"].(map[string]any)["name"].(string), t["spec"].(map[string]any)
}

func volumeDV(t *testing.T, m map[string]any, name string) string {
	t.Helper()
	for _, v := range template(m)["volumes"].([]any) {
		vol := v.(map[string]any)
		if vol["name"] == name {
			return vol["dataVolume"].(map[string]any)["name"].(string)
		}
	}
	t.Fatalf("volume %q not found", name)
	return ""
}

func TestMigrateVolume(t *testing.T) {
	out, err := ApplyEdit([]byte(vmWithDataVolumes), "alpha", "web", VMEdit{
		MigrateVolumes: []model.VolumeMigration{{Name: "rootdisk", StorageClass: "fast"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustParse(t, out)

	name, spec := dvTemplate(m, 0)
	if name != "web-rootdisk-mig-1" {
		t.Errorf("template not renamed: %q", name)
	}
	storage := spec["storage"].(map[string]any)
	if storage["storageClassName"] != "fast" {
		t.Errorf("storage class not changed: %v", storage["storageClassName"])
	}
	if storage["resources"].(map[string]any)["requests"].(map[string]any)["storage"] != "30Gi" {
		t.Errorf("size disturbed:\n%s", out)
	}
	// The destination provisions blank — the registry source must be gone.
	if _, ok := spec["source"].(map[string]any)["blank"]; !ok {
		t.Errorf("source not replaced with blank:\n%s", out)
	}
	if strings.Contains(string(out), "registry:") {
		t.Errorf("old source left behind:\n%s", out)
	}
	if got := volumeDV(t, m, "rootdisk"); got != "web-rootdisk-mig-1" {
		t.Errorf("volume not repointed: %q", got)
	}
	if specOf(m)["updateVolumesStrategy"] != "Migration" {
		t.Errorf("updateVolumesStrategy not set:\n%s", out)
	}
	// The other disk must be untouched.
	if name, _ := dvTemplate(m, 1); name != "web-data" {
		t.Errorf("unrelated template disturbed: %q", name)
	}
	if got := volumeDV(t, m, "data"); got != "web-data" {
		t.Errorf("unrelated volume disturbed: %q", got)
	}
}

// A disk on the cluster default class has no storageClassName — the edit must
// insert one. Its source is already blank and must be kept as-is.
func TestMigrateVolumeInsertsClass(t *testing.T) {
	out, err := ApplyEdit([]byte(vmWithDataVolumes), "alpha", "web", VMEdit{
		MigrateVolumes: []model.VolumeMigration{{Name: "data", StorageClass: "fast"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustParse(t, out)
	name, spec := dvTemplate(m, 1)
	if name != "web-data-mig-1" {
		t.Errorf("template not renamed: %q", name)
	}
	storage := spec["storage"].(map[string]any)
	if storage["storageClassName"] != "fast" {
		t.Errorf("storage class not inserted: %v", storage)
	}
	if _, ok := spec["source"].(map[string]any)["blank"]; !ok {
		t.Errorf("blank source disturbed:\n%s", out)
	}
	if strings.Count(string(out), "blank: {}") != strings.Count(vmWithDataVolumes, "blank: {}") {
		t.Errorf("blank source duplicated or dropped:\n%s", out)
	}
}

// A second migration of the same disk cycles the -mig-N suffix instead of
// accreting, and rewrites the existing updateVolumesStrategy in place.
func TestMigrateVolumeRepeated(t *testing.T) {
	once, err := ApplyEdit([]byte(vmWithDataVolumes), "alpha", "web", VMEdit{
		MigrateVolumes: []model.VolumeMigration{{Name: "rootdisk", StorageClass: "fast"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ApplyEdit(once, "alpha", "web", VMEdit{
		MigrateVolumes: []model.VolumeMigration{{Name: "rootdisk", StorageClass: "standard"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustParse(t, out)
	name, spec := dvTemplate(m, 0)
	if name != "web-rootdisk-mig-2" {
		t.Errorf("suffix not cycled: %q", name)
	}
	if spec["storage"].(map[string]any)["storageClassName"] != "standard" {
		t.Errorf("class not changed back:\n%s", out)
	}
	if got := volumeDV(t, m, "rootdisk"); got != "web-rootdisk-mig-2" {
		t.Errorf("volume not repointed: %q", got)
	}
	if strings.Count(string(out), "updateVolumesStrategy") != 1 {
		t.Errorf("updateVolumesStrategy duplicated:\n%s", out)
	}
}

// Volumes that aren't DataVolume-backed (or reference no template) are left
// alone entirely — including the updateVolumesStrategy marker.
func TestMigrateVolumeNonDataVolume(t *testing.T) {
	out, err := ApplyEdit([]byte(vmWithDataVolumes), "alpha", "web", VMEdit{
		MigrateVolumes: []model.VolumeMigration{{Name: "cloudinit", StorageClass: "fast"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != vmWithDataVolumes {
		t.Errorf("non-DV migration must be a no-op:\n%s", out)
	}
}

// The legacy pvc-spec template form: the class lands under spec.pvc.
func TestMigrateVolumePVCSpec(t *testing.T) {
	src := strings.ReplaceAll(vmWithDataVolumes, "      storage:", "      pvc:")
	out, err := ApplyEdit([]byte(src), "alpha", "web", VMEdit{
		MigrateVolumes: []model.VolumeMigration{{Name: "rootdisk", StorageClass: "fast"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustParse(t, out)
	name, spec := dvTemplate(m, 0)
	if name != "web-rootdisk-mig-1" {
		t.Errorf("template not renamed: %q", name)
	}
	if spec["pvc"].(map[string]any)["storageClassName"] != "fast" {
		t.Errorf("pvc storage class not changed:\n%s", out)
	}
}

func TestChangesForEditMigrateVolumes(t *testing.T) {
	current := model.VM{Disks: []model.Disk{
		{Name: "rootdisk", Type: "dataVolume", StorageClass: "standard"},
		{Name: "data", Type: "dataVolume"},
	}}
	changes := ChangesForEdit(current, VMEdit{MigrateVolumes: []model.VolumeMigration{
		{Name: "rootdisk", StorageClass: "fast"},
		{Name: "data", StorageClass: "fast"},
	}})
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %v", changes)
	}
	if changes[0].Field != "Disk rootdisk storage" || changes[0].From != "standard" || changes[0].To != "fast" {
		t.Errorf("unexpected change: %+v", changes[0])
	}
	if changes[1].From != "cluster default" {
		t.Errorf("default-class disk should read 'cluster default': %+v", changes[1])
	}
}
