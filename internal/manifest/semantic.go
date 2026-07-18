package manifest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/epheo/dotvirt/internal/model"
)

// ChangesForEdit renders a VMEdit as semantic model.Change items, relative to the VM's
// current state (parsed from its source manifest). It mirrors what ApplyEdit
// will do, so the preview matches the eventual diff — without showing YAML.
func ChangesForEdit(current model.VM, edit VMEdit) []model.Change {
	var out []model.Change

	if edit.Power != nil && *edit.Power != string(current.Power) {
		out = append(out, model.Change{Field: "Power", Action: "change", From: string(current.Power), To: *edit.Power})
	}
	out = append(out, sizingChanges(current, edit)...)
	if edit.Preference != nil && *edit.Preference != current.Preference {
		out = append(out, model.Change{Field: "Preference", Action: "change", From: current.Preference, To: *edit.Preference})
	}

	for _, k := range sortedKeys(edit.SetLabels) {
		v := edit.SetLabels[k]
		if old, ok := current.Labels[k]; ok {
			if old != v {
				out = append(out, model.Change{Field: "Label " + k, Action: "change", From: old, To: v})
			}
		} else {
			out = append(out, model.Change{Field: "Label " + k, Action: "add", To: v})
		}
	}
	for _, k := range sortedStrings(edit.RemoveLabels) {
		if old, ok := current.Labels[k]; ok {
			out = append(out, model.Change{Field: "Label " + k, Action: "remove", From: old})
		}
	}

	if edit.DRSExclude != nil && *edit.DRSExclude != current.DRSExclude {
		from, to := "rebalanced", "excluded"
		if !*edit.DRSExclude {
			from, to = to, from
		}
		out = append(out, model.Change{Field: "DRS", Action: "change", From: from, To: to})
	}
	if edit.EvictionStrategy != nil && *edit.EvictionStrategy != current.EvictionStrategy {
		out = append(out, model.Change{Field: "Eviction strategy", Action: "change",
			From: orClusterDefault(current.EvictionStrategy), To: orClusterDefault(*edit.EvictionStrategy)})
	}
	out = append(out, schedulingChanges(current, edit)...)

	for _, d := range edit.AddDisks {
		to := fmt.Sprintf("%s (%s)", d.Name, d.Size)
		if d.StorageClass != "" {
			to = fmt.Sprintf("%s (%s, %s)", d.Name, d.Size, d.StorageClass)
		}
		out = append(out, model.Change{Field: "Disk", Action: "add", To: to})
	}
	for _, name := range edit.RemoveDisks {
		out = append(out, model.Change{Field: "Disk", Action: "remove", From: name})
	}
	for _, n := range edit.AddNetworks {
		out = append(out, model.Change{Field: "Network", Action: "add", To: n.Name})
	}
	for _, name := range edit.RemoveNetworks {
		out = append(out, model.Change{Field: "Network", Action: "remove", From: name})
	}
	for _, mv := range edit.MigrateVolumes {
		from := ""
		for _, d := range current.Disks {
			if d.Name == mv.Name {
				from = d.StorageClass
				break
			}
		}
		out = append(out, model.Change{Field: "Disk " + mv.Name + " storage", Action: "change",
			From: orClusterDefault(from), To: mv.StorageClass})
	}
	return out
}

// schedulingChanges renders placement edits against the VM's current policy,
// skipping no-ops the same way applySchedulingRules' upsert would.
func schedulingChanges(current model.VM, edit VMEdit) []model.Change {
	var out []model.Change
	curGroups := map[string]model.PlacementGroup{}
	var curPin []string
	if current.Scheduling != nil {
		for _, g := range current.Scheduling.Groups {
			curGroups[g.Name] = g
		}
		curPin = current.Scheduling.Pin
	}
	for _, g := range edit.AddGroups {
		to := groupDesc(g)
		if old, ok := curGroups[g.Name]; ok {
			if from := groupDesc(old); from != to {
				out = append(out, model.Change{Field: "Placement group " + g.Name, Action: "change", From: from, To: to})
			}
		} else {
			out = append(out, model.Change{Field: "Placement group " + g.Name, Action: "add", To: to})
		}
	}
	for _, n := range sortedStrings(edit.RemoveGroups) {
		if old, ok := curGroups[n]; ok {
			out = append(out, model.Change{Field: "Placement group " + n, Action: "remove", From: groupDesc(old)})
		}
	}
	if edit.Pin != nil {
		from, to := strings.Join(curPin, ", "), strings.Join(*edit.Pin, ", ")
		switch {
		case from == to:
		case from == "":
			out = append(out, model.Change{Field: "Host pinning", Action: "add", To: to})
		case to == "":
			out = append(out, model.Change{Field: "Host pinning", Action: "remove", From: from})
		default:
			out = append(out, model.Change{Field: "Host pinning", Action: "change", From: from, To: to})
		}
	}
	return out
}

// groupDesc names a placement group's behavior for the preview.
func groupDesc(g model.PlacementGroup) string {
	mode := "keep together"
	if g.Mode == "apart" {
		mode = "keep apart"
	}
	if g.Strict {
		return mode + ", strict"
	}
	return mode + ", preferred"
}

// sizingChanges renders the CPU/memory/instancetype part of an edit, honoring the
// instancetype⇄inline mutual exclusion that applySizing enforces — so the preview
// never shows an inline cpu/memory change that will actually be stripped, nor
// hides the removal of the representation being replaced (e.g. a heal that only
// strips a stray inline block, or a mode switch). Mirrors applySizing's outcome.
func sizingChanges(current model.VM, edit VMEdit) []model.Change {
	mode := ""
	if edit.Sizing != nil {
		mode = *edit.Sizing
	}
	// Will the result be sized by an instancetype? (Same decision as applySizing.)
	usesInstancetype := false
	switch mode {
	case "custom":
		usesInstancetype = false
	case "instancetype":
		usesInstancetype = true
	default:
		usesInstancetype = current.Instancetype != "" || (edit.Instancetype != nil && *edit.Instancetype != "")
	}

	var out []model.Change
	if usesInstancetype {
		// Instance type owns sizing; any inline cpu/memory is stripped.
		if edit.Instancetype != nil && *edit.Instancetype != current.Instancetype {
			out = append(out, model.Change{Field: "Instance type", Action: "change", From: current.Instancetype, To: *edit.Instancetype})
		}
		if current.CPUCores != 0 {
			out = append(out, model.Change{Field: "CPU", Action: "remove", From: fmt.Sprintf("%d vCPU", current.CPUCores)})
		}
		if current.Memory != "" {
			out = append(out, model.Change{Field: "Memory", Action: "remove", From: current.Memory})
		}
	} else {
		// Inline cpu/memory owns sizing; any instancetype is removed.
		if current.Instancetype != "" {
			out = append(out, model.Change{Field: "Instance type", Action: "remove", From: current.Instancetype})
		}
		if edit.CPUCores != nil && *edit.CPUCores != current.CPUCores {
			out = append(out, model.Change{Field: "CPU", Action: "change",
				From: fmt.Sprintf("%d vCPU", current.CPUCores), To: fmt.Sprintf("%d vCPU", *edit.CPUCores)})
		}
		if edit.Memory != nil && *edit.Memory != current.Memory {
			out = append(out, model.Change{Field: "Memory", Action: "change", From: current.Memory, To: *edit.Memory})
		}
	}
	return out
}

// DiffVMs renders the difference between two parsed VMs (e.g. running vs main)
// as semantic model.Change items — used for drift detail. "From" is the a side
// (e.g. main / desired), "To" is the b side (e.g. running / actual).
func DiffVMs(a, b model.VM) []model.Change {
	var out []model.Change
	cmp := func(field, av, bv string) {
		if av != bv {
			out = append(out, model.Change{Field: field, Action: "change", From: av, To: bv})
		}
	}
	cmp("Power", string(a.Power), string(b.Power))
	if a.CPUCores != b.CPUCores {
		cmp("CPU", fmt.Sprintf("%d vCPU", a.CPUCores), fmt.Sprintf("%d vCPU", b.CPUCores))
	}
	cmp("Memory", a.Memory, b.Memory)
	cmp("Instance type", a.Instancetype, b.Instancetype)
	cmp("Preference", a.Preference, b.Preference)
	if a.DRSExclude != b.DRSExclude {
		cmp("DRS", drsLabel(a.DRSExclude), drsLabel(b.DRSExclude))
	}
	if a.EvictionStrategy != b.EvictionStrategy {
		cmp("Eviction strategy", orClusterDefault(a.EvictionStrategy), orClusterDefault(b.EvictionStrategy))
	}

	// Labels present on one side only or differing.
	for _, k := range sortedKeys(a.Labels) {
		av := a.Labels[k]
		if bv, ok := b.Labels[k]; ok {
			cmp("Label "+k, av, bv)
		} else {
			out = append(out, model.Change{Field: "Label " + k, Action: "remove", From: av})
		}
	}
	for _, k := range sortedKeys(b.Labels) {
		if _, ok := a.Labels[k]; !ok {
			out = append(out, model.Change{Field: "Label " + k, Action: "add", To: b.Labels[k]})
		}
	}

	cmp("Host pinning", strings.Join(schedPinOf(a), ", "), strings.Join(schedPinOf(b), ", "))
	diffNamedSet("Placement group", groupDescsOf(a), groupDescsOf(b), &out)

	diffNamedSet("Disk", diskNamesOf(a), diskNamesOf(b), &out)
	diffNamedSet("Network", nicNamesOf(a), nicNamesOf(b), &out)
	return out
}

func schedPinOf(v model.VM) []string {
	if v.Scheduling == nil {
		return nil
	}
	return v.Scheduling.Pin
}

func groupDescsOf(v model.VM) []string {
	if v.Scheduling == nil {
		return nil
	}
	var n []string
	for _, g := range v.Scheduling.Groups {
		n = append(n, fmt.Sprintf("%s (%s)", g.Name, groupDesc(g)))
	}
	return n
}

func diskNamesOf(v model.VM) []string {
	var n []string
	for _, d := range v.Disks {
		label := d.Name
		if d.Size != "" {
			label = fmt.Sprintf("%s (%s)", d.Name, d.Size)
		}
		n = append(n, label)
	}
	return n
}

func nicNamesOf(v model.VM) []string {
	var n []string
	for _, x := range v.Networks {
		n = append(n, x.Name)
	}
	return n
}

// diffNamedSet reports items added/removed between two lists (by value).
func diffNamedSet(field string, a, b []string, out *[]model.Change) {
	as, bs := toSet(a), toSet(b)
	for _, x := range a {
		if !bs[x] {
			*out = append(*out, model.Change{Field: field, Action: "remove", From: x})
		}
	}
	for _, x := range b {
		if !as[x] {
			*out = append(*out, model.Change{Field: field, Action: "add", To: x})
		}
	}
}

func toSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, x := range s {
		m[x] = true
	}
	return m
}

// drsLabel names a VM's DRS participation for the preview.
func drsLabel(excluded bool) string {
	if excluded {
		return "excluded"
	}
	return "rebalanced"
}

// orClusterDefault labels an unset evictionStrategy for the preview.
func orClusterDefault(s string) string {
	if s == "" {
		return "cluster default"
	}
	return s
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
