package manifest

import (
	"errors"
	"reflect"
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
		out, err := ApplyEdit([]byte(in), "vm-health-gitops", "vm-health-gitops", "vm-health", VMEdit{SetLabels: set})
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
		out, err := ApplyEdit([]byte(realisticVM), "vm-health-gitops", "vm-health-gitops", "vm-health", edit)
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
		if _, err := ApplyEdit([]byte(realisticVM), "vm-health-gitops", "vm-health-gitops", "vm-health", edit); !errors.Is(err, model.ErrInvalid) {
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
		{"multi-line scalar", blockScalar, "spans lines", VMEdit{Memory: ptr("4Gi")}},
		{"migration of a non-DataVolume disk", noInterfaces, "rootdisk", VMEdit{MigrateVolumes: []model.VolumeMigration{{Name: "rootdisk", StorageClass: "fast"}}}},
	}
	for _, c := range cases {
		out, err := ApplyEdit([]byte(c.in), "alpha", "alpha", "web", c.edit)
		if !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: want ErrConflict naming %q, got %v\n%s", c.name, c.msg, err, out)
		}
	}
}

// Deleting a key of a flow mapping that does not hold it is not a splice, so
// the flow guard leaves such edits alone.
func TestEditLeavesUntouchedFlowAlone(t *testing.T) {
	out, err := ApplyEdit([]byte(vmFlowCPU), "alpha", "alpha", "web", VMEdit{Memory: ptr("2Gi"), SetLabels: map[string]string{"env": "prod"}})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	if !strings.Contains(string(out), "cpu: {cores: 2, sockets: 1}") || !strings.Contains(string(out), "guest: 2Gi") {
		t.Fatalf("unexpected edit:\n%s", out)
	}
}

// guardVM holds one of everything an edit can touch: labels, inline sizing,
// a DataVolume disk, a pod NIC, a placement group.
const guardVM = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
  labels:
    app: web
spec:
  runStrategy: Always
  dataVolumeTemplates:
  - metadata:
      name: web-data
    spec:
      storage:
        resources:
          requests:
            storage: 10Gi
  template:
    metadata:
      labels:
        group.scheduling.dotvirt.io/tier: together
    spec:
      evictionStrategy: LiveMigrate
      domain:
        cpu:
          sockets: 1
        memory:
          guest: 1Gi
        devices:
          disks:
          - name: data
            disk:
              bus: virtio
          interfaces:
          - name: default
            masquerade: {}
      networks:
      - name: default
        pod: {}
      volumes:
      - name: data
        dataVolume:
          name: web-data
`

// guardVMWithInstancetype is guardVM wrongly carrying both representations,
// the one a sizing switch to custom acts on.
var guardVMWithInstancetype = strings.Replace(guardVM, "  runStrategy: Always\n", "  runStrategy: Always\n  instancetype:\n    name: u1.small\n", 1)

// The read-back and the scope check are what stand between a splice the
// layout defeated and a PR that announces it, so both must know every VMEdit
// field. Each sample sets one field to a value the unedited manifest does not
// hold: the read-back must refuse the unedited file, the edit must apply, and
// the scope check must refuse the applied file once something no edit owns
// changes beside it. A field added to VMEdit without a sample here fails.
func TestVerifyEditCoversEveryField(t *testing.T) {
	samples := map[string]struct {
		base string
		edit VMEdit
	}{
		"Power":            {guardVM, VMEdit{Power: ptr("Off")}},
		"CPUCores":         {guardVM, VMEdit{CPUCores: ptr(4)}},
		"Memory":           {guardVM, VMEdit{Memory: ptr("4Gi")}},
		"Instancetype":     {guardVM, VMEdit{Instancetype: ptr("u1.small")}},
		"Preference":       {guardVM, VMEdit{Preference: ptr("fedora")}},
		"Sizing":           {guardVMWithInstancetype, VMEdit{Sizing: ptr("custom")}},
		"SetLabels":        {guardVM, VMEdit{SetLabels: map[string]string{"env": "prod"}}},
		"RemoveLabels":     {guardVM, VMEdit{RemoveLabels: []string{"app"}}},
		"DRSExclude":       {guardVM, VMEdit{DRSExclude: ptr(true)}},
		"EvictionStrategy": {guardVM, VMEdit{EvictionStrategy: ptr("None")}},
		"AddDisks":         {guardVM, VMEdit{AddDisks: []model.DiskAdd{{Name: "extra", Size: "1Gi"}}}},
		"RemoveDisks":      {guardVM, VMEdit{RemoveDisks: []string{"data"}}},
		"AddNetworks":      {guardVM, VMEdit{AddNetworks: []model.NetworkAdd{{Name: "vlan-10"}}}},
		"RemoveNetworks":   {guardVM, VMEdit{RemoveNetworks: []string{"default"}}},
		"MigrateVolumes":   {guardVM, VMEdit{MigrateVolumes: []model.VolumeMigration{{Name: "data", StorageClass: "fast"}}}},
		"Pin":              {guardVM, VMEdit{Pin: &[]string{"n1"}}},
		"AddGroups":        {guardVM, VMEdit{AddGroups: []model.PlacementGroup{{Name: "db", Mode: "apart"}}}},
		"RemoveGroups":     {guardVM, VMEdit{RemoveGroups: []string{"tier"}}},
	}
	if err := verifyEdit([]byte(guardVM), "alpha", "alpha", "web", VMEdit{}); err != nil {
		t.Fatalf("the unedited manifest must verify against an empty edit: %v", err)
	}
	typ := reflect.TypeOf(VMEdit{})
	for i := range typ.NumField() {
		field := typ.Field(i).Name
		s, ok := samples[field]
		if !ok {
			t.Errorf("VMEdit.%s has no sample: add one here, its read-back to verifyEdit and its paths to ownedPaths", field)
			continue
		}
		v := reflect.ValueOf(s.edit)
		for j := range typ.NumField() {
			if set := !v.Field(j).IsZero(); set != (j == i) {
				t.Errorf("sample for %s must set only %s, %s set = %v", field, field, typ.Field(j).Name, set)
			}
		}
		if err := verifyEdit([]byte(s.base), "alpha", "alpha", "web", s.edit); !errors.Is(err, model.ErrConflict) {
			t.Errorf("verifyEdit ignores VMEdit.%s: an unapplied %s passed (%v)", field, field, err)
		}
		out, err := ApplyEdit([]byte(s.base), "alpha", "alpha", "web", s.edit)
		if err != nil {
			t.Errorf("%s: the sample must apply: %v", field, err)
			continue
		}
		damaged := strings.Replace(string(out), "metadata:\n  name: web\n", "metadata:\n  name: web\n  annotations:\n    collateral: x\n", 1)
		if err := verifyScope([]byte(s.base), []byte(damaged), "alpha", "alpha", "web", s.edit); !errors.Is(err, model.ErrConflict) {
			t.Errorf("%s: collateral damage beside the edit passed the scope check (%v)", field, err)
		}
	}
}

// An added or listed item is read back by every field it carries: a sample
// that differs from guardVM's item in one field must fail the read-back, and
// the one matching it in all must pass, so the refusal is that field's.
func TestVerifyEditCoversEveryItemField(t *testing.T) {
	cases := map[reflect.Type]struct {
		match  VMEdit
		differ map[string]VMEdit
	}{
		reflect.TypeOf(model.DiskAdd{}): {
			VMEdit{AddDisks: []model.DiskAdd{{Name: "data", Size: "10Gi"}}},
			map[string]VMEdit{
				"Name":         {AddDisks: []model.DiskAdd{{Name: "extra", Size: "10Gi"}}},
				"Size":         {AddDisks: []model.DiskAdd{{Name: "data", Size: "20Gi"}}},
				"StorageClass": {AddDisks: []model.DiskAdd{{Name: "data", Size: "10Gi", StorageClass: "fast"}}},
			},
		},
		reflect.TypeOf(model.NetworkAdd{}): {
			VMEdit{AddNetworks: []model.NetworkAdd{{Name: model.PodNetwork, Interface: "default"}}},
			map[string]VMEdit{
				"Name":      {AddNetworks: []model.NetworkAdd{{Name: "alpha/vlan-10", Interface: "default"}}},
				"Interface": {AddNetworks: []model.NetworkAdd{{Name: model.PodNetwork, Interface: "nic-1"}}},
			},
		},
		reflect.TypeOf(model.VolumeMigration{}): {
			VMEdit{MigrateVolumes: []model.VolumeMigration{{Name: "data"}}},
			map[string]VMEdit{
				"Name":         {MigrateVolumes: []model.VolumeMigration{{Name: "other"}}},
				"StorageClass": {MigrateVolumes: []model.VolumeMigration{{Name: "data", StorageClass: "fast"}}},
			},
		},
		reflect.TypeOf(model.PlacementGroup{}): {
			VMEdit{AddGroups: []model.PlacementGroup{{Name: "tier", Mode: "together"}}},
			map[string]VMEdit{
				"Name":   {AddGroups: []model.PlacementGroup{{Name: "db", Mode: "together"}}},
				"Mode":   {AddGroups: []model.PlacementGroup{{Name: "tier", Mode: "apart"}}},
				"Strict": {AddGroups: []model.PlacementGroup{{Name: "tier", Mode: "together", Strict: true}}},
			},
		},
	}
	for typ, c := range cases {
		if err := verifyEdit([]byte(guardVM), "alpha", "alpha", "web", c.match); err != nil {
			t.Errorf("%s: the matching sample must pass: %v", typ.Name(), err)
		}
		for i := range typ.NumField() {
			field := typ.Field(i).Name
			edit, ok := c.differ[field]
			if !ok {
				t.Errorf("%s.%s has no sample: add one here and its read-back to verifyEdit", typ.Name(), field)
				continue
			}
			if err := verifyEdit([]byte(guardVM), "alpha", "alpha", "web", edit); !errors.Is(err, model.ErrConflict) {
				t.Errorf("verifyEdit ignores %s.%s (%v)", typ.Name(), field, err)
			}
		}
	}
}
