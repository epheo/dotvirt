package manifest

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

func TestChangesForEdit(t *testing.T) {
	current := model.VM{
		Power:    model.PowerOn,
		CPUCores: 1,
		Memory:   "1Gi",
		Labels:   map[string]string{"app": "web"},
	}
	changes := ChangesForEdit(current, VMEdit{
		Memory:         ptr("2Gi"),
		CPUCores:       ptr(1), // unchanged — must NOT appear
		SetLabels:      map[string]string{"app": "web2", "tier": "front"},
		RemoveLabels:   []string{"gone"}, // not present — must NOT appear
		AddDisks:       []model.DiskAdd{{Name: "data", Size: "20Gi"}},
		RemoveNetworks: []string{"old"},
	})

	want := map[string]model.Change{
		"Memory":     {Field: "Memory", Action: "change", From: "1Gi", To: "2Gi"},
		"Label app":  {Field: "Label app", Action: "change", From: "web", To: "web2"},
		"Label tier": {Field: "Label tier", Action: "add", To: "front"},
		"Disk":       {Field: "Disk", Action: "add", To: "data (20Gi)"},
		"Network":    {Field: "Network", Action: "remove", From: "old"},
	}
	got := map[string]model.Change{}
	for _, c := range changes {
		got[c.Field] = c
	}
	if _, ok := got["CPU"]; ok {
		t.Error("unchanged CPU should not produce a change")
	}
	if _, ok := got["Label gone"]; ok {
		t.Error("removing an absent label should not produce a change")
	}
	for field, w := range want {
		if got[field] != w {
			t.Errorf("%s: got %+v, want %+v", field, got[field], w)
		}
	}
}

func TestChangesForEditSizing(t *testing.T) {
	byField := func(cs []model.Change) map[string]model.Change {
		m := map[string]model.Change{}
		for _, c := range cs {
			m[c.Field] = c
		}
		return m
	}

	// Heal: an instancetype VM wrongly carrying inline cpu/memory, edited with just
	// Sizing:instancetype. The preview must show the inline cpu/memory being removed
	// (previously it showed nothing, contradicting the committed diff).
	t.Run("heal shows inline removal", func(t *testing.T) {
		got := byField(ChangesForEdit(
			model.VM{Instancetype: "u1.medium", CPUCores: 2, Memory: "4Gi"},
			VMEdit{Sizing: ptr("instancetype")},
		))
		if c := got["CPU"]; c.Action != "remove" || c.From != "2 vCPU" {
			t.Errorf("CPU: got %+v, want remove 2 vCPU", c)
		}
		if c := got["Memory"]; c.Action != "remove" || c.From != "4Gi" {
			t.Errorf("Memory: got %+v, want remove 4Gi", c)
		}
		if _, ok := got["Instance type"]; ok {
			t.Errorf("unchanged instance type should not appear: %+v", got["Instance type"])
		}
	})

	// Instancetype -> custom: instance type removed, inline cpu/memory set.
	t.Run("to custom shows instancetype removal", func(t *testing.T) {
		got := byField(ChangesForEdit(
			model.VM{Instancetype: "u1.medium"},
			VMEdit{Sizing: ptr("custom"), CPUCores: ptr(4), Memory: ptr("8Gi")},
		))
		if c := got["Instance type"]; c.Action != "remove" || c.From != "u1.medium" {
			t.Errorf("Instance type: got %+v, want remove u1.medium", c)
		}
		if c := got["Memory"]; c.Action != "change" || c.To != "8Gi" {
			t.Errorf("Memory: got %+v, want change to 8Gi", c)
		}
	})

	// Custom -> instancetype: instance type set, inline cpu/memory removed.
	t.Run("to instancetype shows inline removal", func(t *testing.T) {
		got := byField(ChangesForEdit(
			model.VM{CPUCores: 2, Memory: "4Gi"},
			VMEdit{Sizing: ptr("instancetype"), Instancetype: ptr("u1.large")},
		))
		if c := got["Instance type"]; c.Action != "change" || c.To != "u1.large" {
			t.Errorf("Instance type: got %+v, want change to u1.large", c)
		}
		if c := got["CPU"]; c.Action != "remove" {
			t.Errorf("CPU: got %+v, want remove", c)
		}
	})

	// Adopt-style edit (no Sizing) that sets CPUCores on an instancetype VM: the
	// backend safety net strips it, so the preview must NOT show a phantom CPU change.
	t.Run("no phantom cpu change on instancetype VM", func(t *testing.T) {
		got := byField(ChangesForEdit(
			model.VM{Instancetype: "u1.medium"},
			VMEdit{CPUCores: ptr(2)},
		))
		if _, ok := got["CPU"]; ok {
			t.Errorf("phantom CPU change shown for instancetype VM: %+v", got["CPU"])
		}
	})
}

func TestDiffVMs(t *testing.T) {
	main := model.VM{Power: model.PowerOn, Memory: "1Gi", Disks: []model.Disk{{Name: "rootdisk"}}}
	running := model.VM{Power: model.PowerOff, Memory: "1Gi", Disks: []model.Disk{
		{Name: "rootdisk"}, {Name: "extra", Size: "5Gi"},
	}}
	changes := DiffVMs(main, running)

	var power, disk *model.Change
	for i := range changes {
		switch changes[i].Field {
		case "Power":
			power = &changes[i]
		case "Disk":
			disk = &changes[i]
		}
	}
	if power == nil || power.From != "On" || power.To != "Off" {
		t.Errorf("power drift wrong: %+v", power)
	}
	if disk == nil || disk.Action != "add" || disk.To != "extra (5Gi)" {
		t.Errorf("disk drift wrong: %+v", disk)
	}
}
