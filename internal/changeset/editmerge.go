package changeset

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// stageEdit lays a VM edit over the one already staged for that VM, so a
// partial edit (a power toggle, a storage move, one inline field) adds to the
// staged change instead of replacing it. A field back at the base branch's
// value drops out: staging git's own value unstages that field, and an edit
// left with nothing unstages the VM.
func (c *Coordinator) stageEdit(id auth.Identity, proj project.ProjectInfo, e draft.Entry) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	current, declared, err := findVM(read, c.baseBranch, e.Namespace, e.Name)
	if err != nil {
		return model.DraftView{}, err
	}
	err = c.store.Update(id.Username, proj.Name, e.Key(), func(staged *draft.Entry) (*draft.Entry, error) {
		edit := *e.Edit
		if staged != nil {
			if staged.Kind != draft.KindEdit || staged.Edit == nil {
				return nil, fmt.Errorf("%w: %s/%s has a staged %s waiting; propose or unstage it before editing its settings",
					model.ErrConflict, e.Namespace, e.Name, stagedChange(*staged))
			}
			edit = overlayEdit(*staged.Edit, edit)
		}
		if declared {
			edit = withoutBase(edit, current)
		}
		if edit.Empty() {
			return nil, nil
		}
		next := e
		next.Edit = &edit
		return &next, nil
	})
	if err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// stagedChange names a staged entry a field edit cannot extend.
func stagedChange(e draft.Entry) string {
	switch {
	case e.Kind != draft.KindEdit:
		return string(e.Kind)
	case e.FromVersion != "":
		return "restore to " + e.FromVersion
	}
	return "manifest edit"
}

// overlayEdit is staged with next laid over it: next's scalars win, labels
// merge by key, and add/remove lists merge by name, where adding what is
// staged for removal (or the reverse) cancels the earlier intent. A sizing
// change in one representation replaces a staged one in the other: inline
// cpu/memory and an instance type cannot both apply.
func overlayEdit(staged, next model.VMEdit) model.VMEdit {
	out := staged
	if rep := sizingRep(next); rep != "" && rep != sizingRep(staged) {
		out.Sizing, out.CPUCores, out.Memory, out.Instancetype = nil, nil, nil, nil
	}
	out.Power = pick(out.Power, next.Power)
	out.CPUCores = pick(out.CPUCores, next.CPUCores)
	out.Memory = pick(out.Memory, next.Memory)
	out.Instancetype = pick(out.Instancetype, next.Instancetype)
	out.Preference = pick(out.Preference, next.Preference)
	out.Sizing = pick(out.Sizing, next.Sizing)
	out.DRSExclude = pick(out.DRSExclude, next.DRSExclude)
	out.EvictionStrategy = pick(out.EvictionStrategy, next.EvictionStrategy)
	out.Pin = pick(out.Pin, next.Pin)

	out.SetLabels = maps.Clone(out.SetLabels)
	for _, k := range next.RemoveLabels {
		delete(out.SetLabels, k)
	}
	out.RemoveLabels = union(without(out.RemoveLabels, slices.Collect(maps.Keys(next.SetLabels))), next.RemoveLabels)
	for k, v := range next.SetLabels {
		if out.SetLabels == nil {
			out.SetLabels = map[string]string{}
		}
		out.SetLabels[k] = v
	}

	diskName := func(d model.DiskAdd) string { return d.Name }
	out.AddDisks = upsert(dropNamed(out.AddDisks, next.RemoveDisks, diskName), next.AddDisks, diskName)
	out.RemoveDisks = union(without(out.RemoveDisks, names(next.AddDisks, diskName)), next.RemoveDisks)

	netName := func(n model.NetworkAdd) string { return n.Name }
	out.AddNetworks = upsert(dropNamed(out.AddNetworks, next.RemoveNetworks, netName), next.AddNetworks, netName)
	out.RemoveNetworks = union(without(out.RemoveNetworks, names(next.AddNetworks, netName)), next.RemoveNetworks)

	groupName := func(g model.PlacementGroup) string { return g.Name }
	out.AddGroups = upsert(dropNamed(out.AddGroups, next.RemoveGroups, groupName), next.AddGroups, groupName)
	out.RemoveGroups = union(without(out.RemoveGroups, names(next.AddGroups, groupName)), next.RemoveGroups)

	out.MigrateVolumes = upsert(out.MigrateVolumes, next.MigrateVolumes, func(m model.VolumeMigration) string { return m.Name })
	return out
}

// sizingRep is the representation an edit sizes the VM in: "instancetype",
// "custom" (inline cpu/memory), or "" when it leaves sizing alone.
func sizingRep(e model.VMEdit) string {
	switch {
	case e.Sizing != nil:
		return *e.Sizing
	case e.Instancetype != nil && *e.Instancetype != "":
		return "instancetype"
	case e.CPUCores != nil || e.Memory != nil:
		return "custom"
	}
	return ""
}

// withoutBase drops what would change nothing on vm, by the same comparisons
// manifest.ChangesForEdit makes. Disk, network and storage moves are always
// changes there, so they stay.
func withoutBase(e model.VMEdit, vm model.VM) model.VMEdit {
	if e.Power != nil && *e.Power == string(vm.Power) {
		e.Power = nil
	}
	if e.Sizing != nil && *e.Sizing == sizedAs(vm) {
		e.Sizing = nil
	}
	// An explicit representation switch writes its block whole.
	if e.Sizing == nil {
		if e.CPUCores != nil && *e.CPUCores == vm.CPUCores {
			e.CPUCores = nil
		}
		if e.Memory != nil && *e.Memory == vm.Memory {
			e.Memory = nil
		}
		if e.Instancetype != nil && *e.Instancetype == vm.Instancetype {
			e.Instancetype = nil
		}
	}
	if e.Preference != nil && *e.Preference == vm.Preference {
		e.Preference = nil
	}
	if e.DRSExclude != nil && *e.DRSExclude == vm.DRSExclude {
		e.DRSExclude = nil
	}
	if e.EvictionStrategy != nil && *e.EvictionStrategy == vm.EvictionStrategy {
		e.EvictionStrategy = nil
	}

	set := map[string]string{}
	for k, v := range e.SetLabels {
		if old, ok := vm.Labels[k]; !ok || old != v {
			set[k] = v
		}
	}
	e.SetLabels = nil
	if len(set) > 0 {
		e.SetLabels = set
	}
	e.RemoveLabels = slices.DeleteFunc(slices.Clone(e.RemoveLabels), func(k string) bool {
		_, ok := vm.Labels[k]
		return !ok
	})

	var groups []model.PlacementGroup
	var pin []string
	if vm.Scheduling != nil {
		groups, pin = vm.Scheduling.Groups, vm.Scheduling.Pin
	}
	if e.Pin != nil && strings.Join(*e.Pin, ", ") == strings.Join(pin, ", ") {
		e.Pin = nil
	}
	e.AddGroups = slices.DeleteFunc(slices.Clone(e.AddGroups), func(g model.PlacementGroup) bool {
		return slices.Contains(groups, g)
	})
	e.RemoveGroups = slices.DeleteFunc(slices.Clone(e.RemoveGroups), func(n string) bool {
		return !slices.ContainsFunc(groups, func(g model.PlacementGroup) bool { return g.Name == n })
	})
	return e
}

// sizedAs is the representation vm is sized in, "" when it carries both (a
// VM an instance-type switch must heal).
func sizedAs(vm model.VM) string {
	switch {
	case vm.Instancetype == "":
		return "custom"
	case vm.CPUCores == 0 && vm.Memory == "":
		return "instancetype"
	}
	return ""
}

// pick is next when set, else staged.
func pick[T any](staged, next *T) *T {
	if next != nil {
		return next
	}
	return staged
}

// upsert is staged with each of next replacing the staged item of its name.
func upsert[T any](staged, next []T, name func(T) string) []T {
	out := dropNamed(staged, names(next, name), name)
	return append(out, next...)
}

// dropNamed is items minus those named in drop.
func dropNamed[T any](items []T, drop []string, name func(T) string) []T {
	return slices.DeleteFunc(slices.Clone(items), func(it T) bool { return slices.Contains(drop, name(it)) })
}

func names[T any](items []T, name func(T) string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = name(it)
	}
	return out
}

// union is a followed by what b adds, each name once.
func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// without is a minus the names in drop.
func without(a, drop []string) []string {
	return slices.DeleteFunc(slices.Clone(a), func(s string) bool { return slices.Contains(drop, s) })
}
