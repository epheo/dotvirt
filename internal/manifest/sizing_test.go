package manifest

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// itVM is web sized by instancetype u1.small; %s is the domain's sizing
// lines, each indented under domain.
const itVM = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
spec:
  runStrategy: Always
  instancetype:
    name: u1.small
  template:
    spec:
      domain:
%s        devices:
          disks:
          - name: rootdisk
            disk:
              bus: virtio
`

// sizingValues holds a value for every sizingFields path.
var sizingValues = map[string]string{
	"cpu.sockets":               "2",
	"cpu.cores":                 "2",
	"cpu.threads":               "2",
	"memory.guest":              "4Gi",
	"resources.requests.cpu":    `"2"`,
	"resources.requests.memory": "4Gi",
	"resources.limits.cpu":      `"2"`,
	"resources.limits.memory":   "4Gi",
	"cpu.model":                 "host-passthrough",
	"cpu.dedicatedCPUPlacement": "true",
	"cpu.isolateEmulatorThread": "true",
	"cpu.numa":                  "{guestMappingPassthrough: {}}",
	"cpu.realtime":              `{mask: "0"}`,
	"memory.hugepages":          "{pageSize: 2Mi}",
	"memory.maxGuest":           "8Gi",
}

// block renders path = value as the block lines under domain.
func block(path, value string) string {
	var b strings.Builder
	keys := strings.Split(path, ".")
	for i, k := range keys {
		b.WriteString(strings.Repeat(" ", 8+2*i) + k + ":")
		if i == len(keys)-1 {
			b.WriteString(" " + value)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func domainOf(t *testing.T, out []byte) map[string]any {
	t.Helper()
	d, _ := lookup(decoded(t, out), "spec", "template", "spec", "domain").(map[string]any)
	return d
}

// Each sizing field beside an instancetype: an always field goes on every
// edit; a shared one stays while the VM keeps its instancetype, or switches
// to one that leaves it, and goes on a switch to one that sets it or that was
// not read for the switch. A block the strip empties goes with it.
func TestInstancetypeEditStripsWhatCollides(t *testing.T) {
	larger := ptr("u1.large")
	for _, f := range sizingFields {
		value, ok := sizingValues[f.path]
		if !ok {
			t.Fatalf("%s has no sample value", f.path)
		}
		in := fmt.Sprintf(itVM, block(f.path, value))
		for _, c := range []struct {
			name   string
			edit   VMEdit
			target Target
			strip  bool
		}{
			{"kept", VMEdit{Power: ptr("Off")}, Target{}, f.always},
			{"kept, sizing restated", VMEdit{Sizing: ptr("instancetype")}, Target{}, f.always},
			{"switched to one setting it", VMEdit{Instancetype: larger}, Target{Name: "u1.large", Read: true, Sets: []string{f.path}}, true},
			{"switched to one leaving it", VMEdit{Instancetype: larger}, Target{Name: "u1.large", Read: true}, f.always},
			{"switched, unread", VMEdit{Instancetype: larger}, Target{}, true},
			{"switched, read for another", VMEdit{Instancetype: larger}, Target{Name: "u1.medium", Read: true}, true},
			{"switched, read for another kind", VMEdit{Instancetype: larger}, Target{Kind: "VirtualMachineInstancetype", Name: "u1.large", Read: true}, true},
		} {
			out, err := ApplyEditFor([]byte(in), "alpha", "alpha", "web", c.edit, c.target)
			if err != nil {
				t.Errorf("%s, %s: %v", f.path, c.name, err)
				continue
			}
			keys := strings.Split(f.path, ".")
			got := lookup(domainOf(t, out), keys...)
			switch {
			case c.strip && got != nil:
				t.Errorf("%s, %s: kept, KubeVirt refuses it beside the instancetype:\n%s", f.path, c.name, out)
			case c.strip && lookup(domainOf(t, out), keys[0]) != nil:
				t.Errorf("%s, %s: the emptied %s block stays:\n%s", f.path, c.name, keys[0], out)
			case !c.strip && !reflect.DeepEqual(got, lookup(domainOf(t, []byte(in)), keys...)):
				t.Errorf("%s, %s: lost, KubeVirt admits it here:\n%s", f.path, c.name, out)
			}
		}
	}
}

// A VM keeping its instancetype loses only what collides with any
// instancetype: what else shares a block with it stays, byte for byte, and a
// block goes only once the strip empties it.
func TestInstancetypeEditKeepsBlocksItDoesNotEmpty(t *testing.T) {
	sizingLines := `        cpu:
          sockets: 2
          model: host-passthrough
          maxSockets: 8
        memory:
          guest: 4Gi
          hugepages:
            pageSize: 2Mi
        resources:
          overcommitGuestOverhead: true
          requests:
            cpu: "2"
            memory: 4Gi
          limits:
            cpu: "2"
            memory: 4Gi
`
	kept := `        cpu:
          model: host-passthrough
          maxSockets: 8
        memory:
          hugepages:
            pageSize: 2Mi
        resources:
          overcommitGuestOverhead: true
`
	out, err := ApplyEdit([]byte(fmt.Sprintf(itVM, sizingLines)), "alpha", "alpha", "web", VMEdit{Power: ptr("Off")})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(fmt.Sprintf(itVM, kept), "runStrategy: Always", "runStrategy: Halted", 1)
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

// A custom-sized VM switched to an instancetype keeps the shared fields it
// does not set, and loses the ones it does.
func TestCustomToInstancetypeKeepsWhatTheTargetLeaves(t *testing.T) {
	in := strings.Replace(fmt.Sprintf(itVM, `        cpu:
          sockets: 2
          model: host-passthrough
        memory:
          guest: 4Gi
          hugepages:
            pageSize: 1Gi
`), "  instancetype:\n    name: u1.small\n", "", 1)
	edit := VMEdit{Sizing: ptr("instancetype"), Instancetype: ptr("cx1.large")}
	out, err := ApplyEditFor([]byte(in), "alpha", "alpha", "web", edit, Target{Name: "cx1.large", Read: true, Sets: []string{"cpu.model"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"memory": map[string]any{"hugepages": map[string]any{"pageSize": "1Gi"}}, "devices": lookup(domainOf(t, []byte(in)), "devices")}
	if got := domainOf(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("domain = %#v\nwant %#v\n%s", got, want, out)
	}
	if got := lookup(decoded(t, out), "spec", "instancetype", "name"); got != "cx1.large" {
		t.Errorf("instancetype = %v", got)
	}
}

// A flow-style block the strip only thins cannot be spliced: the edit is
// refused rather than dropping the block whole, kept fields with it.
func TestInstancetypeEditRefusesThinningAFlowBlock(t *testing.T) {
	in := fmt.Sprintf(itVM, "        cpu: {sockets: 2, model: host-passthrough}\n")
	if out, err := ApplyEdit([]byte(in), "alpha", "alpha", "web", VMEdit{Power: ptr("Off")}); !errors.Is(err, model.ErrConflict) {
		t.Errorf("want ErrConflict, got %v\n%s", err, out)
	}
}

// The read-back refuses a strip that did not land, and the scope check a
// removal of what the VM keeps beside its instancetype.
func TestSizingChecksHoldTheStrip(t *testing.T) {
	in := fmt.Sprintf(itVM, "        memory:\n          hugepages:\n            pageSize: 2Mi\n        resources:\n          limits:\n            memory: 4Gi\n")
	edit := VMEdit{Power: ptr("Off")}
	strip := stripOf(t, in, edit)
	if err := verifyStripped([]byte(in), "alpha", "alpha", "web", strip); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "resources.limits.memory") {
		t.Errorf("a limit left beside the instancetype passed the read-back: %v", err)
	}
	out, err := ApplyEdit([]byte(in), "alpha", "alpha", "web", edit)
	if err != nil {
		t.Fatal(err)
	}
	lost := strings.Replace(string(out), "        memory:\n          hugepages:\n            pageSize: 2Mi\n", "", 1)
	if lost == string(out) {
		t.Fatalf("hugepages not kept:\n%s", out)
	}
	if err := verifyScope([]byte(in), []byte(lost), "alpha", "alpha", "web", edit, strip); !errors.Is(err, model.ErrConflict) {
		t.Errorf("a kept hugepages removed passed the scope check: %v", err)
	}
}

// The preview lists every removal the strip makes that model.VM cannot show,
// never a shared field the VM keeps, and says when it removes them all
// because the target could not be read.
func TestStripChangesDescribeTheOutcome(t *testing.T) {
	in := []byte(fmt.Sprintf(itVM, `        cpu:
          sockets: 2
          model: host-passthrough
        memory:
          guest: 4Gi
          hugepages:
            pageSize: 2Mi
        resources:
          requests:
            memory: 4Gi
          limits:
            cpu: "2"
`))
	rows := func(edit VMEdit, target Target) []string {
		var out []string
		for _, c := range StripChanges(in, "alpha", "alpha", "web", edit, target) {
			if c.Action != "remove" || !c.Restart {
				t.Errorf("%+v: want a restart-flagged removal", c)
			}
			out = append(out, c.Field+": "+c.From)
		}
		return out
	}
	larger := ptr("u1.large")
	for name, c := range map[string]struct {
		edit   VMEdit
		target Target
		want   []string
	}{
		"kept": {VMEdit{Power: ptr("Off")}, Target{}, []string{"Memory request: 4Gi", "CPU limit: 2"}},
		"switched, read": {VMEdit{Instancetype: larger}, Target{Name: "u1.large", Read: true, Sets: []string{"memory.hugepages"}},
			[]string{"Memory request: 4Gi", "CPU limit: 2", "Hugepages: pageSize: 2Mi"}},
		"switched, unread": {VMEdit{Instancetype: larger}, Target{},
			[]string{"Memory request: 4Gi", "CPU limit: 2", "CPU model: host-passthrough (could not read u1.large)", "Hugepages: pageSize: 2Mi (could not read u1.large)"}},
		"custom": {VMEdit{Sizing: ptr("custom"), CPUCores: ptr(4)}, Target{}, nil},
	} {
		if got := rows(c.edit, c.target); !slices.Equal(got, c.want) {
			t.Errorf("%s: rows = %q, want %q", name, got, c.want)
		}
	}
	vms, err := ParseVMs("web.yaml", in, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range ChangesForEdit(vms[0], VMEdit{Power: ptr("Off")}) {
		if c.Field == "Hugepages" || c.Field == "CPU model" {
			t.Errorf("a kept shared field reads as changed: %+v", c)
		}
	}
}

func TestSwitchTarget(t *testing.T) {
	custom := strings.Replace(fmt.Sprintf(itVM, ""), "  instancetype:\n    name: u1.small\n", "", 1)
	namespaced := strings.Replace(fmt.Sprintf(itVM, ""), "    name: u1.small\n", "    kind: VirtualMachineInstancetype\n    name: u1.small\n", 1)
	for name, c := range map[string]struct {
		in   string
		edit VMEdit
		want Target
		ok   bool
	}{
		"kept":               {fmt.Sprintf(itVM, ""), VMEdit{Power: ptr("Off")}, Target{}, false},
		"same name restated": {fmt.Sprintf(itVM, ""), VMEdit{Sizing: ptr("instancetype"), Instancetype: ptr("u1.small")}, Target{}, false},
		"renamed":            {fmt.Sprintf(itVM, ""), VMEdit{Instancetype: ptr("u1.large")}, Target{Name: "u1.large"}, true},
		"namespaced kind":    {namespaced, VMEdit{Instancetype: ptr("u1.large")}, Target{Kind: "VirtualMachineInstancetype", Name: "u1.large"}, true},
		"set on custom":      {custom, VMEdit{Sizing: ptr("instancetype"), Instancetype: ptr("u1.large")}, Target{Name: "u1.large"}, true},
		"unnamed":            {custom, VMEdit{Sizing: ptr("instancetype")}, Target{}, false},
		"to custom":          {fmt.Sprintf(itVM, ""), VMEdit{Sizing: ptr("custom"), Instancetype: ptr("u1.large")}, Target{}, false},
		"VM absent":          {custom, VMEdit{Instancetype: ptr("u1.large")}, Target{}, false},
	} {
		vmName := "web"
		if name == "VM absent" {
			vmName = "ghost"
		}
		got, ok := SwitchTarget([]byte(c.in), "alpha", "alpha", vmName, c.edit)
		if ok != c.ok || ok && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: SwitchTarget = %+v, %v; want %+v, %v", name, got, ok, c.want, c.ok)
		}
	}
}

func TestSharedSet(t *testing.T) {
	it := map[string]any{"spec": map[string]any{
		"cpu":    map[string]any{"guest": int64(4), "model": "host-passthrough", "dedicatedCPUPlacement": false},
		"memory": map[string]any{"guest": "8Gi", "hugepages": map[string]any{"pageSize": "1Gi"}},
	}}
	if got, want := SharedSet(it), []string{"cpu.model", "cpu.dedicatedCPUPlacement", "memory.hugepages"}; !slices.Equal(got, want) {
		t.Errorf("SharedSet = %q, want %q", got, want)
	}
	if got := SharedSet(map[string]any{}); got != nil {
		t.Errorf("an object without spec sets %q", got)
	}
}
