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
		AddDisks:       []DiskAdd{{Name: "data", Size: "20Gi"}},
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
