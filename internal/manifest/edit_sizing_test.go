package manifest

import (
	"strings"
	"testing"
)

// shdgVM is the broken shape that fails KubeVirt's webhook: an instancetype AND
// inline domain.cpu/domain.memory at the same time. This is what an Edit Settings
// CPU/memory change used to splice onto a wizard-created (instancetype) VM.
const shdgVM = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: shdg
  namespace: tenant-a
spec:
  runStrategy: Always
  instancetype:
    name: u1.medium
  preference:
    name: fedora
  template:
    spec:
      domain:
        cpu:
          cores: 2
        memory:
          guest: 4Gi
        devices:
          disks:
          - name: rootdisk
            disk:
              bus: virtio
`

// assertNotBoth fails if the manifest carries both an instancetype and inline
// cpu/memory — the mutually-exclusive sizing the webhook rejects.
func assertNotBoth(t *testing.T, out []byte) {
	t.Helper()
	m := mustParse(t, out)
	spec, _ := m["spec"].(map[string]any)
	if spec == nil {
		return
	}
	_, hasIT := spec["instancetype"]

	var dom map[string]any
	if tmpl, _ := spec["template"].(map[string]any); tmpl != nil {
		if ts, _ := tmpl["spec"].(map[string]any); ts != nil {
			dom, _ = ts["domain"].(map[string]any)
		}
	}
	hasInline := false
	if dom != nil {
		_, cpu := dom["cpu"]
		_, mem := dom["memory"]
		hasInline = cpu || mem
		if res, _ := dom["resources"].(map[string]any); res != nil {
			if reqs, _ := res["requests"].(map[string]any); reqs != nil {
				_, rc := reqs["cpu"]
				_, rm := reqs["memory"]
				hasInline = hasInline || rc || rm
			}
		}
	}
	if hasIT && hasInline {
		t.Errorf("manifest has BOTH instancetype and inline cpu/memory (webhook would reject):\n%s", out)
	}
}

// Switching an instancetype VM to custom sizing drops the instancetype and writes
// inline cpu/memory — never leaving both. Preference is independent and stays.
func TestSizingInstancetypeToCustom(t *testing.T) {
	out, err := ApplyEdit([]byte(vmWithDevices), "alpha", "web", VMEdit{
		Sizing:   ptr("custom"),
		CPUCores: ptr(4),
		Memory:   ptr("8Gi"),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "instancetype:") {
		t.Errorf("instancetype not removed when switching to custom:\n%s", s)
	}
	if !strings.Contains(s, "cores: 4") || !strings.Contains(s, "guest: 8Gi") {
		t.Errorf("inline cpu/memory not written:\n%s", s)
	}
	if !strings.Contains(s, "name: fedora") {
		t.Errorf("preference wrongly removed:\n%s", s)
	}
	assertNotBoth(t, out)
}

// Switching a custom-sized VM to an instancetype writes the ref and strips the
// inline cpu/memory the instancetype now owns.
func TestSizingCustomToInstancetype(t *testing.T) {
	out, err := ApplyEdit([]byte(realisticVM), "vm-health-gitops", "vm-health", VMEdit{
		Sizing:       ptr("instancetype"),
		Instancetype: ptr("u1.medium"),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "name: u1.medium") {
		t.Errorf("instancetype not written:\n%s", s)
	}
	if strings.Contains(s, "cores:") || strings.Contains(s, "guest:") {
		t.Errorf("inline cpu/memory not stripped:\n%s", s)
	}
	assertNotBoth(t, out)
}

// Re-staging the sizing of a broken VM (instancetype mode, name unchanged so no
// Instancetype field) strips the stray inline block — healing shdg in place.
func TestSizingHealsConflictingVM(t *testing.T) {
	out, err := ApplyEdit([]byte(shdgVM), "tenant-a", "shdg", VMEdit{
		Sizing: ptr("instancetype"),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "name: u1.medium") {
		t.Errorf("instancetype lost while healing:\n%s", s)
	}
	if strings.Contains(s, "cpu:") || strings.Contains(s, "memory:") {
		t.Errorf("inline cpu/memory not stripped while healing:\n%s", s)
	}
	// The devices block must survive the strip untouched.
	if !strings.Contains(s, "bus: virtio") || !strings.Contains(s, "name: rootdisk") {
		t.Errorf("devices disturbed by sizing strip:\n%s", s)
	}
	assertNotBoth(t, out)
}

// A non-sizing edit (no Sizing mode) on a VM that already has an instancetype must
// never apply inline cpu/memory and should normalize away any stray inline block.
func TestSizingDefaultBranchNeverWritesBoth(t *testing.T) {
	// Power-only edit heals the conflicting shdg as a side effect.
	out, err := ApplyEdit([]byte(shdgVM), "tenant-a", "shdg", VMEdit{Power: ptr("Off")})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "runStrategy: Halted") {
		t.Errorf("power not applied:\n%s", s)
	}
	if strings.Contains(s, "cpu:") || strings.Contains(s, "memory:") {
		t.Errorf("default branch left inline cpu/memory on an instancetype VM:\n%s", s)
	}
	assertNotBoth(t, out)

	// A stray CPUCores on an instancetype VM (no Sizing) is ignored, not spliced in.
	out2, err := ApplyEdit([]byte(vmWithDevices), "alpha", "web", VMEdit{CPUCores: ptr(8)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out2), "cores: 8") {
		t.Errorf("inline cpu wrongly written onto an instancetype VM:\n%s", out2)
	}
	assertNotBoth(t, out2)
}

// The legacy resources.requests cpu/memory are stripped too when an instancetype
// takes over sizing.
func TestSizingStripsLegacyResources(t *testing.T) {
	const itLegacy = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: legacy-it
  namespace: alpha
spec:
  runStrategy: Always
  instancetype:
    name: u1.small
  template:
    spec:
      domain:
        resources:
          requests:
            cpu: "2"
            memory: 2Gi
        devices:
          disks:
          - name: rootdisk
            disk:
              bus: virtio
`
	out, err := ApplyEdit([]byte(itLegacy), "alpha", "legacy-it", VMEdit{Sizing: ptr("instancetype")})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "cpu: ") || strings.Contains(s, "memory: 2Gi") {
		t.Errorf("legacy resources.requests cpu/memory not stripped:\n%s", s)
	}
	assertNotBoth(t, out)
}

// Custom->instancetype where cpu/memory are the LAST lines of spec: the queued
// instancetype insert is anchored on the memory line that stripInlineSizing then
// deletes. The insert must still land at spec indent and yield valid YAML.
func TestSizingInsertAnchoredOnDeletedLine(t *testing.T) {
	const vmCpuMemLast = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: tail
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
        cpu:
          cores: 1
        memory:
          guest: 1Gi
`
	out, err := ApplyEdit([]byte(vmCpuMemLast), "alpha", "tail", VMEdit{
		Sizing:       ptr("instancetype"),
		Instancetype: ptr("u1.medium"),
	})
	if err != nil {
		t.Fatal(err)
	}
	mustParse(t, out) // asserts valid YAML
	s := string(out)
	if strings.Contains(s, "cores:") || strings.Contains(s, "guest:") {
		t.Errorf("inline not stripped:\n%s", s)
	}
	if !strings.Contains(s, "name: u1.medium") {
		t.Errorf("instancetype not inserted:\n%s", s)
	}
	assertNotBoth(t, out)
}

// Flow-style cpu (`cpu: {cores: 2}`) and an extra resources.requests key must be
// handled: strip exactly cpu/memory, preserve the unrelated key, stay valid YAML.
func TestSizingStripsFlowStyleAndPreservesExtras(t *testing.T) {
	const vmFlowAndExtras = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: flow
  namespace: alpha
spec:
  runStrategy: Always
  instancetype:
    name: u1.small
  template:
    spec:
      domain:
        cpu: {cores: 2}
        resources:
          requests:
            cpu: "2"
            memory: 2Gi
            hugepages: huge
        devices:
          disks:
          - name: rootdisk
            disk:
              bus: virtio
`
	out, err := ApplyEdit([]byte(vmFlowAndExtras), "alpha", "flow", VMEdit{Sizing: ptr("instancetype")})
	if err != nil {
		t.Fatal(err)
	}
	mustParse(t, out)
	s := string(out)
	if strings.Contains(s, "cores:") {
		t.Errorf("flow-style cpu not stripped:\n%s", s)
	}
	if !strings.Contains(s, "hugepages: huge") {
		t.Errorf("unrelated requests key wrongly removed:\n%s", s)
	}
	assertNotBoth(t, out)
}
