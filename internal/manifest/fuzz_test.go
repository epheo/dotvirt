package manifest

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// The line editor splices structured edits into user-authored YAML — the
// wildest input in the system (hand-written manifests, adopted exports,
// arbitrary comments and indentation). The invariant fuzzed here: whatever the
// input, ApplyEdit either refuses with an error or produces YAML the parser
// still accepts. A "successful" edit that corrupts the manifest would land in
// a PR and only fail at Argo apply time, far from its cause.
func FuzzApplyEdit(f *testing.F) {
	f.Add([]byte(realisticVM), "vm-health-gitops", "vm-health", "2Gi", 2, "data", "10Gi", true)
	f.Add([]byte(realisticVM), "vm-health-gitops", "vm-health", "", 0, "", "", false)
	f.Add([]byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: v\nspec: {}\n"),
		"ns", "v", "1Gi", 1, "d", "1Gi", true)
	f.Add([]byte("not: yaml: at: all ["), "ns", "v", "1Gi", 1, "", "", false)

	f.Fuzz(func(t *testing.T, content []byte, ns, name, memory string, cpu int, disk, size string, on bool) {
		edit := model.VMEdit{}
		if memory != "" {
			edit.Memory = &memory
		}
		if cpu > 0 && cpu < 4096 {
			edit.CPUCores = &cpu
		}
		power := "Off"
		if on {
			power = "On"
		}
		edit.Power = &power
		if disk != "" && size != "" {
			edit.AddDisks = []model.DiskAdd{{Name: disk, Size: size}}
		}

		// The staging layer only edits manifests the inventory already parsed,
		// so the contract is parse-PRESERVATION: a parseable input stays
		// parseable. Unparseable input may be refused or passed through.
		if _, perr := ParseVMs("vms/fuzz.yaml", content, ns); perr != nil {
			return
		}
		out, err := ApplyEdit(content, ns, name, edit)
		if err != nil {
			return // refusing an edit is always allowed
		}
		if _, perr := ParseVMs("vms/fuzz.yaml", out, ns); perr != nil {
			t.Fatalf("edit broke a manifest that parsed before it: %v\ninput:\n%s\noutput:\n%s",
				perr, content, out)
		}
	})
}
