package changeset

import (
	"testing"

	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
)

// TestEditToMatchConverges is the guard for the parallel field lists behind
// drift adoption: every difference DiffVMs can report must be reconcilable by
// editToMatch + ApplyEdit, or drift shows in the UI that Adopt can never
// converge. A field added to DiffVMs without an editToMatch counterpart fails
// the variant exercising it.
func TestEditToMatchConverges(t *testing.T) {
	const base = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
  labels:
    app: web
spec:
  runStrategy: Always
  template:
    metadata:
      labels:
        kubevirt.io/domain: web
    spec:
      evictionStrategy: LiveMigrate
      domain:
        cpu:
          cores: 1
        memory:
          guest: 1Gi
`
	parse := func(content string) model.VM {
		t.Helper()
		vms, err := manifest.ParseVMs("web.yaml", []byte(content), "alpha")
		if err != nil || len(vms) != 1 {
			t.Fatalf("parse: %v (%d VMs)", err, len(vms))
		}
		return vms[0]
	}
	from := parse(base)

	// Each variant is the base with ONE aspect moved — the shape of real
	// running-vs-desired drift.
	variants := map[string]func(vm model.VM) model.VM{
		"power": func(vm model.VM) model.VM { vm.Power = model.PowerOff; return vm },
		"sizing": func(vm model.VM) model.VM {
			vm.CPUCores, vm.Memory = 4, "4Gi"
			return vm
		},
		"labels": func(vm model.VM) model.VM {
			vm.Labels = map[string]string{"env": "prod"} // app removed, env added
			return vm
		},
		"drs exclude": func(vm model.VM) model.VM { vm.DRSExclude = true; return vm },
		"eviction strategy set": func(vm model.VM) model.VM {
			vm.EvictionStrategy = "None"
			return vm
		},
		"eviction strategy cleared": func(vm model.VM) model.VM {
			vm.EvictionStrategy = ""
			return vm
		},
	}
	for name, mutate := range variants {
		to := mutate(parse(base))
		if len(manifest.DiffVMs(from, to)) == 0 && name != "eviction strategy cleared" {
			t.Fatalf("%s: variant produced no drift to reconcile", name)
		}
		edit := editToMatch(from, to)
		if len(manifest.DiffVMs(from, to)) > 0 && edit.Empty() {
			t.Fatalf("%s: DiffVMs reports drift but editToMatch is empty — unadoptable drift", name)
		}
		out, err := manifest.ApplyEdit([]byte(base), "alpha", "web", edit)
		if err != nil {
			t.Fatalf("%s: ApplyEdit: %v", name, err)
		}
		got := parse(string(out))
		if d := manifest.DiffVMs(got, to); len(d) != 0 {
			t.Errorf("%s: drift did not converge after adopt, remaining: %+v\n%s", name, d, out)
		}
	}
}
