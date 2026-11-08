package changeset

import (
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// stageEdit lays a VM edit over the one already staged for that VM, so a
// partial edit (a power toggle, a storage move, one inline field) adds to the
// staged change instead of replacing it. A field back at the base branch's
// value drops out: staging git's own value unstages that field, and an edit
// left with nothing unstages the VM. The incoming values are checked before
// the merge, and the merged edit is dry-run against the VM's file before it is
// written; the file is read once, before the store lock (see editTarget), and
// so is an instancetype the edit switches the VM to (readSwitch).
func (c *Coordinator) stageEdit(id auth.Identity, proj project.ProjectInfo, e draft.Entry) (model.DraftView, error) {
	if err := manifest.ValidateEdit(*e.Edit); err != nil {
		return model.DraftView{}, err
	}
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	target, err := c.editTarget(read, e.Namespace, e.Name)
	if err != nil {
		return model.DraftView{}, err
	}
	// The gate's question for a settings edit, asked off the file just read.
	need, err := fieldEditAccess(target.path, target.content, e.Namespace, e.Name)
	if err != nil {
		return model.DraftView{}, err
	}
	if err := c.authorize(id, need); err != nil {
		return model.DraftView{}, err
	}
	fresh := c.readSwitch(id, target, e)
	key := e.Key()
	err = c.store.Apply(id.Username, proj.Name, func(d map[string]draft.Entry) error {
		edit := *e.Edit
		var held *manifest.Target
		if staged, ok := d[key]; ok {
			if !fieldEdit(staged) {
				return fmt.Errorf("%w: %s/%s has a staged %s waiting; propose or unstage it before editing its settings",
					model.ErrConflict, e.Namespace, e.Name, stagedChange(staged))
			}
			edit = overlayEdit(*staged.Edit, edit)
			held = staged.Instancetype
		}
		edit = withoutBase(edit, target.vm)
		if edit.Empty() {
			delete(d, key)
			return nil
		}
		next := e
		next.SourceFile, next.Edit, next.Instancetype = target.path, &edit, nil
		if want, ok := manifest.SwitchTarget(target.content, defaultNamespace(target.path), e.Namespace, e.Name, edit); ok {
			next.Instancetype = readFor(want, fresh, held)
		}
		out, err := manifest.ApplyEditFor(target.content, defaultNamespace(target.path), e.Namespace, e.Name, edit, targetOf(next))
		if err != nil {
			return err
		}
		if err := keepsDeclared(proj, target.path, target.content, out); err != nil {
			return err
		}
		d[key] = next
		return nil
	})
	if err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// readSwitch reads the instancetype e's own edit switches the VM in f to,
// under id's token: one GET per edit naming a new instancetype, none for one
// that leaves sizing alone (the staged read then still answers). It runs
// before the store lock, so a slow apiserver stalls no one else's staging. A
// failed read is nil: the switch then strips every field an instancetype may
// take over, and the view says so.
func (c *Coordinator) readSwitch(id auth.Identity, f vmFile, e draft.Entry) *manifest.Target {
	want, ok := manifest.SwitchTarget(f.content, defaultNamespace(f.path), e.Namespace, e.Name, *e.Edit)
	if !ok || c.instancetypes == nil {
		return nil
	}
	obj, err := c.instancetypes.Instancetype(id, e.Namespace, want.Kind, want.Name)
	if err != nil {
		log.Printf("changeset: read instancetype %s for %s/%s: %v", want.Name, e.Namespace, e.Name, err)
		return nil
	}
	want.Read, want.Sets = true, manifest.SharedSet(obj)
	return &want
}

// readFor is the first of reads made for want, else want unread.
func readFor(want manifest.Target, reads ...*manifest.Target) *manifest.Target {
	for _, r := range reads {
		if r != nil && r.Answers(want) {
			return r
		}
	}
	return &want
}

// targetOf is what e holds of the instancetype its edit switches to.
func targetOf(e draft.Entry) manifest.Target {
	if e.Instancetype == nil {
		return manifest.Target{}
	}
	return *e.Instancetype
}

// fieldEdit reports whether e is a VM field edit, the one kind of entry an
// edit can be laid over.
func fieldEdit(e draft.Entry) bool { return e.Kind == draft.KindEdit && e.Edit != nil }

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
// staged for removal (or the reverse) cancels the earlier intent. A disk or
// network the draft alone adds and then removes leaves nothing behind: base
// has no such device to remove. A sizing change in one representation
// replaces a staged one in the other: inline cpu/memory and an instance type
// cannot both apply.
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
	out.AddDisks, out.RemoveDisks = mergeDevices(out.AddDisks, out.RemoveDisks, next.AddDisks, next.RemoveDisks, diskName)

	// RemoveNetworks names interfaces, so adds merge by theirs.
	netName := func(n model.NetworkAdd) string { return n.InterfaceName() }
	out.AddNetworks, out.RemoveNetworks = mergeDevices(out.AddNetworks, out.RemoveNetworks, next.AddNetworks, next.RemoveNetworks, netName)

	groupName := func(g model.PlacementGroup) string { return g.Name }
	out.AddGroups = upsert(dropNamed(out.AddGroups, next.RemoveGroups, groupName), next.AddGroups, groupName)
	out.RemoveGroups = union(without(out.RemoveGroups, names(next.AddGroups, groupName)), next.RemoveGroups)

	out.MigrateVolumes = upsert(out.MigrateVolumes, next.MigrateVolumes, func(m model.VolumeMigration) string { return m.Name })
	return out
}

// mergeDevices lays next's device adds and removals over staged ones. A
// removal of a device only the staged edit adds cancels that add and is not
// kept, and an add of a device the staged edit removes cancels that removal:
// each pair nets to nothing.
func mergeDevices[T any](adds []T, removes []string, nextAdds []T, nextRemoves []string, name func(T) string) ([]T, []string) {
	staged := names(adds, name)
	var keep []string
	for _, n := range nextRemoves {
		if !slices.Contains(staged, n) {
			keep = append(keep, n)
		}
	}
	cancelled := names(nextAdds, name)
	var fresh []T
	for _, a := range nextAdds {
		if !slices.Contains(removes, name(a)) {
			fresh = append(fresh, a)
		}
	}
	return upsert(dropNamed(adds, nextRemoves, name), fresh, name), union(without(removes, cancelled), keep)
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
