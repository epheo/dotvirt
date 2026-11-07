package manifest

import (
	"errors"
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/model"
)

// argoLabels decodes the VM's labels the way Argo and kubectl read a manifest
// (YAML 1.1 through sigs.k8s.io/yaml), keeping each value's JSON type.
func argoLabels(t *testing.T, out []byte) map[string]any {
	t.Helper()
	var doc struct {
		Metadata struct {
			Labels map[string]any `json:"labels"`
		} `json:"metadata"`
	}
	if err := sigsyaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("decode as Argo would: %v\n%s", err, out)
	}
	return doc.Metadata.Labels
}

// Label values that look like YAML booleans or numbers are valid Kubernetes
// label values, but spliced bare they decode as bool or float and the apply
// fails at Argo sync. They must land as strings, on an existing label map and
// on a VM without one.
func TestEditLabelValuesStayStrings(t *testing.T) {
	set := map[string]string{"env": "true", "ver": "1.0", "flag": "yes", "hex": "0x10", "app": "web"}
	noLabels := strings.Replace(realisticVM, "  labels:\n    app: vm-health\n", "", 1)
	for name, in := range map[string]string{"existing map": realisticVM, "new map": noLabels} {
		out, err := ApplyEdit([]byte(in), "vm-health-gitops", "vm-health", VMEdit{SetLabels: set})
		if err != nil {
			t.Fatalf("%s: ApplyEdit: %v", name, err)
		}
		got := argoLabels(t, out)
		for k, want := range set {
			if v, ok := got[k].(string); !ok || v != want {
				t.Errorf("%s: label %s = %#v, want string %q\n%s", name, k, got[k], want, out)
			}
		}
	}
}

// A label value carrying a newline would splice a second YAML document into
// the file, one that the VM-only parse guard never looks at.
func TestEditRefusesNewlineInjection(t *testing.T) {
	inject := "a\n---\nkind: RoleBinding"
	for name, edit := range map[string]VMEdit{
		"label value": {SetLabels: map[string]string{"app": inject}},
		"label key":   {SetLabels: map[string]string{inject: "x"}},
		"memory":      {Memory: ptr("1Gi\n---\nkind: RoleBinding")},
		"preference":  {Preference: ptr(inject)},
	} {
		out, err := ApplyEdit([]byte(realisticVM), "vm-health-gitops", "vm-health", edit)
		if !errors.Is(err, model.ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v\n%s", name, err, out)
		}
	}
	if _, err := yamlString(inject); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("yamlString must refuse a line break, got %v", err)
	}
}

// The emitter writes plain text only when both readers take it back as the
// same string, so ordinary values keep the file's existing style.
func TestYAMLStringQuotesOnlyWhenNeeded(t *testing.T) {
	plain := []string{"web", "4Gi", "u1.medium", "alpha/vlan-10", "LiveMigrate", "kubevirt.io/domain", "a-b_c.d"}
	for _, s := range plain {
		if got, err := yamlString(s); err != nil || got != s {
			t.Errorf("yamlString(%q) = %q, %v; want it plain", s, got, err)
		}
	}
	quoted := []string{"true", "False", "1.0", "42", "yes", "on", "0x10", "1_000", "~", "null", "", "a: b", "a #b", " pad", "*x", "&x y", "!x", "[a]", "{a}", "-", "<<"}
	for _, s := range quoted {
		got, err := yamlString(s)
		if err != nil {
			t.Errorf("yamlString(%q): %v", s, err)
			continue
		}
		if got == s {
			t.Errorf("yamlString(%q) left it plain", s)
		}
		var v map[string]any
		if err := sigsyaml.Unmarshal([]byte(got+": "+got), &v); err != nil || v[s] != s {
			t.Errorf("yamlString(%q) = %s does not read back: %v %#v", s, got, err, v)
		}
	}
}

// Every value an edit writes is checked before splicing: what the API server
// or KubeVirt would reject fails at the form, not at Argo apply after merge.
func TestEditRefusesInvalidValues(t *testing.T) {
	for name, edit := range map[string]VMEdit{
		"power":              {Power: ptr("on")},
		"zero cpu":           {CPUCores: ptr(0)},
		"memory":             {Memory: ptr("lots")},
		"negative memory":    {Memory: ptr("-1Gi")},
		"instancetype":       {Instancetype: ptr("U1 Large")},
		"empty preference":   {Preference: ptr("")},
		"sizing":             {Sizing: ptr("both")},
		"label key":          {SetLabels: map[string]string{"bad key": "x"}},
		"label value":        {SetLabels: map[string]string{"app": "has space"}},
		"eviction strategy":  {EvictionStrategy: ptr("Sometimes")},
		"disk name":          {AddDisks: []model.DiskAdd{{Name: "Data", Size: "1Gi"}}},
		"disk size":          {AddDisks: []model.DiskAdd{{Name: "data", Size: "big"}}},
		"disk class":         {AddDisks: []model.DiskAdd{{Name: "data", StorageClass: "Fast Disk"}}},
		"network":            {AddNetworks: []model.NetworkAdd{{Name: "a/b/c"}}},
		"network name":       {AddNetworks: []model.NetworkAdd{{Name: "alpha/Bad"}}},
		"migration class":    {MigrateVolumes: []model.VolumeMigration{{Name: "rootdisk"}}},
		"pin host":           {Pin: &[]string{"node 1"}},
		"group name":         {AddGroups: []model.PlacementGroup{{Name: "Web", Mode: "apart"}}},
		"group mode":         {AddGroups: []model.PlacementGroup{{Name: "web", Mode: "near"}}},
		"line break in host": {Pin: &[]string{"n1\n- n2"}},
	} {
		if _, err := ApplyEdit([]byte(realisticVM), "vm-health-gitops", "vm-health", edit); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

// vmFlowCPU writes cpu as a flow mapping: its axes share one line, so a line
// splice on one axis cuts through the others.
const vmFlowCPU = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
spec:
  runStrategy: Always
  template:
    spec:
      domain:
        cpu: {cores: 2, sockets: 1}
        memory:
          guest: 1Gi
        resources:
          requests: {cpu: "2", hugepages: huge}
        devices:
          disks:
          - name: rootdisk
            disk:
              bus: virtio
`

// The editor never silently applies a different change than asked: a layout
// it cannot splice is refused as a conflict naming what to do, never written
// half-applied (the flow-style cpu lost its whole block; a network added to a
// VM without an interfaces list was a no-op the PR body still announced).
func TestEditRefusesWhatItCannotApply(t *testing.T) {
	const noInterfaces = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
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
          image: fedora
`
	const noTemplate = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
spec:
  runStrategy: Always
`
	const blockScalar = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
spec:
  runStrategy: Always
  template:
    spec:
      domain:
        memory:
          guest: |-
            1Gi
`
	cases := []struct {
		name, in, msg string
		edit          VMEdit
	}{
		{"flow cpu", vmFlowCPU, "flow style", VMEdit{CPUCores: ptr(4)}},
		{"flow requests", vmFlowCPU, "flow style", VMEdit{Sizing: ptr("instancetype"), Instancetype: ptr("u1.small")}},
		{"network without interfaces", noInterfaces, "implicit pod network", VMEdit{AddNetworks: []model.NetworkAdd{{Name: "vlan-10"}}}},
		{"cpu without template", noTemplate, "CPU", VMEdit{CPUCores: ptr(2)}},
		{"eviction without template", noTemplate, "eviction strategy", VMEdit{EvictionStrategy: ptr("None")}},
		{"multi-line scalar", blockScalar, "memory", VMEdit{Memory: ptr("4Gi")}},
		{"migration of a non-DataVolume disk", noInterfaces, "rootdisk", VMEdit{MigrateVolumes: []model.VolumeMigration{{Name: "rootdisk", StorageClass: "fast"}}}},
	}
	for _, c := range cases {
		out, err := ApplyEdit([]byte(c.in), "alpha", "web", c.edit)
		if !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: want ErrConflict naming %q, got %v\n%s", c.name, c.msg, err, out)
		}
	}
}

// Deleting a key of a flow mapping that does not hold it is not a splice, so
// the flow guard leaves such edits alone.
func TestEditLeavesUntouchedFlowAlone(t *testing.T) {
	out, err := ApplyEdit([]byte(vmFlowCPU), "alpha", "web", VMEdit{Memory: ptr("2Gi"), SetLabels: map[string]string{"env": "prod"}})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	if !strings.Contains(string(out), "cpu: {cores: 2, sockets: 1}") || !strings.Contains(string(out), "guest: 2Gi") {
		t.Fatalf("unexpected edit:\n%s", out)
	}
}
