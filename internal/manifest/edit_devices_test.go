package manifest

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/model"
)

// vmWithDevices mirrors a real VM manifest with disks, volumes, networks, and
// interfaces — the shape device edits operate on.
const vmWithDevices = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
  labels:
    app: web
spec:
  runStrategy: Always
  instancetype:
    name: u1.medium
  preference:
    name: fedora
  dataVolumeTemplates:
  - metadata:
      name: web-rootdisk
    spec:
      sourceRef:
        kind: DataSource
        name: fedora
        namespace: os-images
      storage:
        resources:
          requests:
            storage: 30Gi
  template:
    spec:
      domain:
        devices:
          disks:
          - name: rootdisk
            disk:
              bus: virtio
          interfaces:
          - name: default
            masquerade: {}
      networks:
      - name: default
        pod: {}
      volumes:
      - name: rootdisk
        dataVolume:
          name: web-rootdisk
`

// vmContainerDisk is a VM with no dataVolumeTemplates (a container-disk import) —
// adding a persistent disk must create the section from scratch.
const vmContainerDisk = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: ctr
  namespace: alpha
spec:
  runStrategy: Always
  template:
    spec:
      domain:
        devices:
          disks:
          - name: rootdisk
            disk:
              bus: virtio
      volumes:
      - name: rootdisk
        containerDisk:
          image: quay.io/example/fedora:latest
`

// mustParse asserts the edited manifest is still valid YAML and returns it.
func mustParse(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatalf("edited manifest is invalid YAML: %v\n%s", err, b)
	}
	return m
}

func TestEditInstancetypePreference(t *testing.T) {
	out, err := ApplyEdit([]byte(vmWithDevices), "alpha", "web", VMEdit{
		Instancetype: ptr("u1.large"),
		Preference:   ptr("rhel.9"),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "name: u1.large") || !strings.Contains(s, "name: rhel.9") {
		t.Errorf("instancetype/preference not updated:\n%s", s)
	}
	if n := changedLines(vmWithDevices, s); n != 2 {
		t.Errorf("expected 2 changed lines, got %d", n)
	}
	mustParse(t, out)
}

func TestEditLabelsUpsertAndRemove(t *testing.T) {
	out, err := ApplyEdit([]byte(vmWithDevices), "alpha", "web", VMEdit{
		SetLabels:    map[string]string{"app": "web2", "tier": "frontend"},
		RemoveLabels: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "app: web2") {
		t.Errorf("label not updated:\n%s", s)
	}
	if !strings.Contains(s, "tier: frontend") {
		t.Errorf("label not added:\n%s", s)
	}
	mustParse(t, out)
}

func TestAddDisk(t *testing.T) {
	out, err := ApplyEdit([]byte(vmWithDevices), "alpha", "web", VMEdit{
		AddDisks: []model.DiskAdd{{Name: "data", Size: "20Gi", StorageClass: "fast"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustParse(t, out)
	if got := len(disksOf(t, m)); got != 2 {
		t.Fatalf("expected 2 disks, got %d:\n%s", got, out)
	}
	s := string(out)
	// The new disk is a persistent, blank DataVolume on the chosen class — not an
	// emptyDisk — with its own dataVolumeTemplates entry.
	for _, want := range []string{"name: web-data", "blank: {}", "storage: 20Gi", "storageClassName: fast"} {
		if !strings.Contains(s, want) {
			t.Errorf("blank DataVolume missing %q:\n%s", want, s)
		}
	}
	if got := len(dvTemplatesOf(t, m)); got != 2 {
		t.Fatalf("expected 2 dataVolumeTemplates (rootdisk + data), got %d:\n%s", got, s)
	}
	// rootdisk must be untouched.
	if !strings.Contains(s, "name: web-rootdisk") {
		t.Error("existing volume disturbed")
	}
}

// A VM without a dataVolumeTemplates section gets one created when a disk is added.
func TestAddDiskCreatesTemplatesSection(t *testing.T) {
	out, err := ApplyEdit([]byte(vmContainerDisk), "alpha", "ctr", VMEdit{
		AddDisks: []model.DiskAdd{{Name: "data", Size: "20Gi", StorageClass: "fast"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustParse(t, out)
	if got := len(dvTemplatesOf(t, m)); got != 1 {
		t.Fatalf("expected a created dataVolumeTemplates section with 1 entry, got %d:\n%s", got, out)
	}
	for _, want := range []string{"name: ctr-data", "blank: {}", "storage: 20Gi", "storageClassName: fast"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestAddNetwork(t *testing.T) {
	out, err := ApplyEdit([]byte(vmWithDevices), "alpha", "web", VMEdit{
		AddNetworks: []model.NetworkAdd{{Name: "tenant-a/mt-bridge"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustParse(t, out)
	if got := len(ifacesOf(t, m)); got != 2 {
		t.Fatalf("expected 2 interfaces, got %d:\n%s", got, out)
	}
	s := string(out)
	if !strings.Contains(s, "networkName: tenant-a/mt-bridge") || !strings.Contains(s, "name: mt-bridge") {
		t.Errorf("network not added:\n%s", s)
	}
}

func TestRemoveDisk(t *testing.T) {
	// First add two disks, then remove one by name; result should have rootdisk + kept.
	added, err := ApplyEdit([]byte(vmWithDevices), "alpha", "web", VMEdit{
		AddDisks: []model.DiskAdd{{Name: "data", Size: "20Gi"}, {Name: "logs", Size: "5Gi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ApplyEdit(added, "alpha", "web", VMEdit{RemoveDisks: []string{"data"}})
	if err != nil {
		t.Fatal(err)
	}
	m := mustParse(t, out)
	names := diskNames(t, m)
	if hasStr(names, "data") {
		t.Errorf("disk 'data' not removed: %v\n%s", names, out)
	}
	if !hasStr(names, "rootdisk") || !hasStr(names, "logs") {
		t.Errorf("removal took the wrong disks: %v\n%s", names, out)
	}
	s := string(out)
	// The 'data' volume AND its dataVolume template must be gone (no orphaned PVC).
	if strings.Contains(s, "web-data") || strings.Contains(s, "storage: 20Gi") {
		t.Errorf("data volume/template not fully removed:\n%s", s)
	}
	if got := len(dvTemplatesOf(t, m)); got != 2 {
		t.Fatalf("expected 2 dataVolumeTemplates (rootdisk + logs), got %d:\n%s", got, s)
	}
	// 'logs' must survive intact.
	if !strings.Contains(s, "web-logs") || !strings.Contains(s, "storage: 5Gi") {
		t.Errorf("logs volume wrongly removed:\n%s", s)
	}
}

// --- helpers to dig into the parsed manifest ---

func template(m map[string]any) map[string]any {
	return m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
}
func disksOf(t *testing.T, m map[string]any) []any {
	t.Helper()
	return template(m)["domain"].(map[string]any)["devices"].(map[string]any)["disks"].([]any)
}
func ifacesOf(t *testing.T, m map[string]any) []any {
	t.Helper()
	return template(m)["domain"].(map[string]any)["devices"].(map[string]any)["interfaces"].([]any)
}
func dvTemplatesOf(t *testing.T, m map[string]any) []any {
	t.Helper()
	dv, _ := m["spec"].(map[string]any)["dataVolumeTemplates"].([]any)
	return dv
}
func diskNames(t *testing.T, m map[string]any) []string {
	t.Helper()
	var out []string
	for _, d := range disksOf(t, m) {
		out = append(out, d.(map[string]any)["name"].(string))
	}
	return out
}
func hasStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
