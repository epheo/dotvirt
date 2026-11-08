package manifest

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/validate"
)

// ValidateEdit refuses an edit carrying a value the API server or KubeVirt
// would reject, naming every such value at once. It needs no manifest, so a
// partial edit can be refused before it is merged or spliced; ApplyEdit runs
// it first.
// Removals only name what the manifest already holds and are never written,
// so they are not checked: a hand-written name outside these rules must stay
// removable.
func ValidateEdit(e VMEdit) error {
	var errs []error
	add := func(err error) { errs = append(errs, err) }
	if e.Power != nil {
		add(validate.RequireOneOf("power", *e.Power, string(model.PowerOn), string(model.PowerOff)))
	}
	if e.CPUCores != nil && *e.CPUCores < 1 {
		add(fmt.Errorf("%w: CPU count %d must be at least 1", model.ErrInvalid, *e.CPUCores))
	}
	if e.Memory != nil {
		add(validate.RequireQuantity("memory", *e.Memory))
	}
	if e.Instancetype != nil {
		add(validate.RequireObjectName("instance type", *e.Instancetype))
	}
	if e.Preference != nil {
		add(validate.RequireObjectName("preference", *e.Preference))
	}
	if e.Sizing != nil {
		add(validate.RequireOneOf("sizing", *e.Sizing, "custom", "instancetype"))
	}
	add(validate.RequireLabels("label", e.SetLabels))
	if e.EvictionStrategy != nil {
		add(validate.RequireOneOf("eviction strategy", *e.EvictionStrategy, "", "None", "LiveMigrate", "LiveMigrateIfPossible", "External"))
	}
	for _, d := range e.AddDisks {
		add(validate.RequireDNS1123("disk name", d.Name))
		if d.Size != "" {
			add(validate.RequireQuantity("disk size", d.Size))
		}
		if d.StorageClass != "" {
			add(validate.RequireObjectName("storage class", d.StorageClass))
		}
	}
	for _, n := range e.AddNetworks {
		if n.Name != model.PodNetwork {
			add(validate.RequireNetworkRef("network", n.Name))
		}
		add(validate.RequireInterfaceName("interface name", n.InterfaceName()))
	}
	for _, m := range e.MigrateVolumes {
		add(validate.RequireObjectName("storage class", m.StorageClass))
	}
	if e.Pin != nil {
		for _, h := range *e.Pin {
			add(validate.RequireObjectName("host", h))
		}
	}
	for _, g := range e.AddGroups {
		add(validate.RequireDNS1123("group name", g.Name))
		add(validate.RequireOneOf("group mode", g.Mode, "together", "apart"))
	}
	return errors.Join(errs...)
}

// verifyEdit re-reads the edited manifest and refuses an edit that did not
// land as asked. Line splicing trusts the document's layout, and a shape it
// does not expect (a multi-line scalar, a missing list, no template, or the
// degenerate indentation fuzzing found) would otherwise corrupt the file or
// apply a different change than the PR description states.
func verifyEdit(out []byte, defaultNS, namespace, name string, edit VMEdit) error {
	vms, err := ParseVMs("", out, defaultNS)
	if err != nil {
		return fmt.Errorf("%w: edit would corrupt the manifest of %s/%s; edit it in git directly: %v", model.ErrConflict, namespace, name, err)
	}
	i := slices.IndexFunc(vms, func(v model.VM) bool { return v.Namespace == namespace && v.Name == name })
	if i < 0 {
		return fmt.Errorf("%w: edit would lose %s/%s from its manifest; edit it in git directly", model.ErrConflict, namespace, name)
	}
	vm := vms[i]

	var missed []string
	want := func(ok bool, what string) {
		if !ok {
			missed = append(missed, what)
		}
	}
	if edit.Power != nil {
		want(string(vm.Power) == *edit.Power, "power")
	}
	verifySizing(vm, edit, want)
	if edit.Preference != nil {
		want(vm.Preference == *edit.Preference, "preference")
	}
	for _, k := range sortedKeys(edit.SetLabels) {
		v, ok := vm.Labels[k]
		want(ok && v == edit.SetLabels[k], "label "+k)
	}
	for _, k := range edit.RemoveLabels {
		_, ok := vm.Labels[k]
		want(!ok, "label "+k+" removal")
	}
	if edit.DRSExclude != nil {
		want(vm.DRSExclude == *edit.DRSExclude, "DRS exclusion")
	}
	if edit.EvictionStrategy != nil {
		want(vm.EvictionStrategy == *edit.EvictionStrategy, "eviction strategy")
	}

	disks := map[string]model.Disk{}
	for _, d := range vm.Disks {
		disks[d.Name] = d
	}
	for _, d := range edit.AddDisks {
		size := d.Size
		if size == "" {
			size = defaultDiskSize
		}
		got, ok := disks[d.Name]
		want(ok && got.Type == "dataVolume" && got.Size == size && got.StorageClass == d.StorageClass, "disk "+d.Name)
	}
	for _, n := range edit.RemoveDisks {
		_, ok := disks[n]
		want(!ok, "disk "+n+" removal")
	}
	for _, m := range edit.MigrateVolumes {
		d, ok := disks[m.Name]
		want(ok && d.StorageClass == m.StorageClass, "storage migration of disk "+m.Name)
	}

	// The implicit pod adapter is KubeVirt's default, not the manifest's: a
	// template left without interfaces reads it back by design.
	nics := map[string]model.NIC{}
	for _, n := range vm.Networks {
		if !n.Implicit {
			nics[n.Name] = n
		}
	}
	for _, n := range edit.AddNetworks {
		nic, ok := nics[n.InterfaceName()]
		want(ok && nic.Network == n.Name, "network "+n.Name)
	}
	for _, n := range edit.RemoveNetworks {
		_, ok := nics[n]
		want(!ok, "network "+n+" removal")
	}

	if edit.Pin != nil {
		want(slices.Equal(schedPinOf(vm), *edit.Pin), "host pinning")
	}
	groups := map[string]model.PlacementGroup{}
	if vm.Scheduling != nil {
		for _, g := range vm.Scheduling.Groups {
			groups[g.Name] = g
		}
	}
	for _, g := range edit.AddGroups {
		got, ok := groups[g.Name]
		want(ok && got == g, "placement group "+g.Name)
	}
	for _, n := range edit.RemoveGroups {
		_, ok := groups[n]
		want(!ok, "placement group "+n+" removal")
	}

	if len(missed) > 0 {
		return fmt.Errorf("%w: the manifest of %s/%s is laid out in a way the editor cannot change (%s would not apply); edit it in git directly",
			model.ErrConflict, namespace, name, strings.Join(missed, ", "))
	}
	return nil
}

// verifySizing checks the sizing outcome applySizing promises, as model.VM
// shows it: beside an instancetype, when the result has one, no inline vCPU
// count or guest memory is left; without one, the inline values asked for are
// what the manifest holds. verifyStripped reads back the rest of the strip.
func verifySizing(vm model.VM, edit VMEdit, want func(bool, string)) {
	mode := ""
	if edit.Sizing != nil {
		mode = *edit.Sizing
	}
	switch mode {
	case "custom":
		want(vm.Instancetype == "", "instance type removal")
	case "instancetype":
		want(vm.Instancetype != "", "instance type")
	}
	if edit.Instancetype != nil && mode != "custom" {
		want(vm.Instancetype == *edit.Instancetype, "instance type")
	}
	if vm.Instancetype != "" {
		want(vm.CPUCores == 0 && vm.Memory == "", "inline CPU count and guest memory removal")
		return
	}
	if edit.CPUCores != nil {
		want(vm.CPUCores == *edit.CPUCores, "CPU")
	}
	if edit.Memory != nil {
		want(vm.Memory == *edit.Memory, "memory")
	}
}

// verifyStripped refuses an edit that left a field of strip in the VM:
// KubeVirt refuses the VM with it beside its instancetype, and model.VM, which
// verifySizing reads, does not carry requests, limits or the shared fields.
func verifyStripped(out []byte, defaultNS, namespace, name string, strip []string) error {
	vm, err := findVM(out, defaultNS, namespace, name)
	if err != nil || vm == nil {
		return nil // verifyEdit refused it already
	}
	domain := domainNode(vm)
	var left []string
	for _, p := range strip {
		if at(domain, p) != nil {
			left = append(left, p+" removal")
		}
	}
	if len(left) > 0 {
		return fmt.Errorf("%w: the manifest of %s/%s is laid out in a way the editor cannot change (%s would not apply); edit it in git directly",
			model.ErrConflict, namespace, name, strings.Join(left, ", "))
	}
	return nil
}
