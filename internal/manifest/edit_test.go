package manifest

import (
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

// realisticVM mirrors the shape dotvirt exports from a live cluster: comments,
// nested domain, runStrategy + memory.guest.
const realisticVM = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: vm-health
  namespace: vm-health-gitops
  labels:
    app: vm-health
spec:
  runStrategy: Always
  template:
    spec:
      domain:
        cpu:
          cores: 1
        memory:
          guest: 1Gi
        devices:
          disks:
          - disk:
              bus: virtio
            name: rootdisk
`

func TestApplyEditMinimalMemory(t *testing.T) {
	out, err := ApplyEdit([]byte(realisticVM), "vm-health-gitops", "vm-health", VMEdit{Memory: ptr("2Gi")})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "guest: 2Gi") {
		t.Fatalf("memory not changed:\n%s", got)
	}
	if strings.Contains(got, "guest: 1Gi") {
		t.Error("old memory value still present")
	}
	// Minimal-diff check: exactly one line differs from the original.
	if n := changedLines(realisticVM, got); n != 1 {
		t.Errorf("expected 1 changed line, got %d:\n%s", n, unifiedish(realisticVM, got))
	}
	// Structure preserved: comment/label/disk untouched.
	for _, must := range []string{"app: vm-health", "bus: virtio", "name: rootdisk", "runStrategy: Always"} {
		if !strings.Contains(got, must) {
			t.Errorf("edit disturbed unrelated content, missing %q", must)
		}
	}
}

func TestApplyEditPowerAndCPU(t *testing.T) {
	out, err := ApplyEdit([]byte(realisticVM), "vm-health-gitops", "vm-health",
		VMEdit{Power: ptr("Off"), CPUCores: ptr(4)})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "runStrategy: Halted") {
		t.Errorf("power not set to Halted:\n%s", got)
	}
	if !strings.Contains(got, "cores: 4") {
		t.Errorf("cpu not set to 4:\n%s", got)
	}
	if n := changedLines(realisticVM, got); n != 2 {
		t.Errorf("expected 2 changed lines (power+cpu), got %d", n)
	}
}

func TestApplyEditLegacyRunningAndMemory(t *testing.T) {
	legacy := `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: legacy
spec:
  running: true
  template:
    spec:
      domain:
        resources:
          requests:
            memory: 512Mi
`
	out, err := ApplyEdit([]byte(legacy), "any", "legacy", VMEdit{Power: ptr("Off"), Memory: ptr("1Gi")})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	got := string(out)
	// Legacy forms edited in place, not duplicated with modern fields.
	if !strings.Contains(got, "running: false") {
		t.Errorf("legacy running not edited:\n%s", got)
	}
	if strings.Contains(got, "runStrategy") {
		t.Error("should not introduce runStrategy when legacy running is present")
	}
	if !strings.Contains(got, "memory: 1Gi") {
		t.Errorf("legacy memory not edited:\n%s", got)
	}
	if strings.Contains(got, "guest:") {
		t.Error("should not introduce memory.guest when legacy resources.requests.memory is present")
	}
}

func TestApplyEditNotFound(t *testing.T) {
	_, err := ApplyEdit([]byte(realisticVM), "wrong", "nope", VMEdit{Memory: ptr("2Gi")})
	if err == nil {
		t.Fatal("expected error for missing VM")
	}
}

// changedLines counts line positions that differ between two texts. The editor
// edits in place without adding/removing lines, so a positional comparison is
// the right measure of diff size for these cases.
func changedLines(a, b string) int {
	al := strings.Split(strings.TrimRight(a, "\n"), "\n")
	bl := strings.Split(strings.TrimRight(b, "\n"), "\n")
	n := len(al)
	if len(bl) > n {
		n = len(bl)
	}
	changed := 0
	for i := 0; i < n; i++ {
		var av, bv string
		if i < len(al) {
			av = al[i]
		}
		if i < len(bl) {
			bv = bl[i]
		}
		if av != bv {
			changed++
		}
	}
	return changed
}

func unifiedish(a, b string) string {
	return "--- before\n" + a + "--- after\n" + b
}
