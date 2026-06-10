package changeset

import (
	"fmt"

	"github.com/epheo/dotvirt/internal/api"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/vmgen"
)

// editFromRequest maps an api.EditRequest into a git.VMEdit.
func editFromRequest(req api.EditRequest) git.VMEdit {
	edit := git.VMEdit{
		Power:          req.Power,
		CPUCores:       req.CPUCores,
		Memory:         req.Memory,
		Instancetype:   req.Instancetype,
		Preference:     req.Preference,
		SetLabels:      req.SetLabels,
		RemoveLabels:   req.RemoveLabels,
		RemoveDisks:    req.RemoveDisks,
		RemoveNetworks: req.RemoveNetworks,
	}
	for _, d := range req.AddDisks {
		edit.AddDisks = append(edit.AddDisks, git.DiskAdd{Name: d.Name, Size: d.Size})
	}
	for _, n := range req.AddNetworks {
		edit.AddNetworks = append(edit.AddNetworks, git.NetworkAdd{Name: n.Name})
	}
	return edit
}

// changesForCreate renders a new-VM spec as "add" semantic items for the draft
// preview, without showing YAML.
func changesForCreate(s vmgen.Spec) []git.Change {
	out := []git.Change{
		{Field: "Create VM", Action: "add", To: s.Namespace + "/" + s.Name},
		{Field: "Instance type", Action: "add", To: s.Instancetype},
		{Field: "Preference", Action: "add", To: s.Preference},
		{Field: "OS image", Action: "add", To: s.OSImage.Name},
	}
	if s.DiskSize != "" {
		out = append(out, git.Change{Field: "Root disk", Action: "add", To: s.DiskSize})
	}
	for _, d := range s.ExtraDisks {
		out = append(out, git.Change{Field: "Disk", Action: "add", To: fmt.Sprintf("%s (%s)", d.Name, d.Size)})
	}
	for _, n := range s.Networks {
		out = append(out, git.Change{Field: "Network", Action: "add", To: n.Name})
	}
	out = append(out, git.Change{Field: "Power", Action: "add", To: powerWord(s.Running)})
	return out
}

func powerWord(running bool) string {
	if running {
		return "On"
	}
	return "Off"
}

// editToMatch builds a VMEdit that transforms `from` (e.g. main/desired) into
// `to` (e.g. running/actual) for the scalar + label + disk/network fields dotvirt
// edits. Used by Adopt to propose the live state into git.
func editToMatch(from, to model.VM) git.VMEdit {
	var edit git.VMEdit
	if from.Power != to.Power && to.Power != model.PowerUnknown {
		p := string(to.Power)
		edit.Power = &p
	}
	if from.CPUCores != to.CPUCores && to.CPUCores != 0 {
		c := to.CPUCores
		edit.CPUCores = &c
	}
	if from.Memory != to.Memory && to.Memory != "" {
		m := to.Memory
		edit.Memory = &m
	}
	if from.Instancetype != to.Instancetype && to.Instancetype != "" {
		it := to.Instancetype
		edit.Instancetype = &it
	}
	if from.Preference != to.Preference && to.Preference != "" {
		pr := to.Preference
		edit.Preference = &pr
	}

	// Labels: set those changed/added in `to`, remove those only in `from`.
	set := map[string]string{}
	for k, v := range to.Labels {
		if from.Labels[k] != v {
			set[k] = v
		}
	}
	if len(set) > 0 {
		edit.SetLabels = set
	}
	for k := range from.Labels {
		if _, ok := to.Labels[k]; !ok {
			edit.RemoveLabels = append(edit.RemoveLabels, k)
		}
	}

	// Disks/networks present only in `to` are added; only in `from` are removed.
	fromDisks, toDisks := diskNameSet(from), diskNameSet(to)
	for name, size := range toDisks {
		if _, ok := fromDisks[name]; !ok {
			edit.AddDisks = append(edit.AddDisks, git.DiskAdd{Name: name, Size: size})
		}
	}
	for name := range fromDisks {
		if _, ok := toDisks[name]; !ok {
			edit.RemoveDisks = append(edit.RemoveDisks, name)
		}
	}
	fromNets, toNets := nicNameSet(from), nicNameSet(to)
	for name, net := range toNets {
		if _, ok := fromNets[name]; !ok {
			edit.AddNetworks = append(edit.AddNetworks, git.NetworkAdd{Name: net})
		}
	}
	for name := range fromNets {
		if _, ok := toNets[name]; !ok {
			edit.RemoveNetworks = append(edit.RemoveNetworks, name)
		}
	}
	return edit
}

func diskNameSet(v model.VM) map[string]string {
	m := map[string]string{}
	for _, d := range v.Disks {
		m[d.Name] = d.Size
	}
	return m
}

func nicNameSet(v model.VM) map[string]string {
	m := map[string]string{}
	for _, n := range v.Networks {
		m[n.Name] = n.Network
	}
	return m
}
