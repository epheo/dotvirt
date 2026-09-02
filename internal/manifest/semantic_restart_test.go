package manifest

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

func TestChangesForEditRestartPlane(t *testing.T) {
	current := model.VM{Power: "On", CPUCores: 2, Memory: "4Gi",
		Disks: []model.Disk{{Name: "root", StorageClass: "slow"}}}
	cores, power, sc := 4, "Off", "fast"
	changes := ChangesForEdit(current, VMEdit{
		Power:          &power,
		CPUCores:       &cores,
		SetLabels:      map[string]string{"tier": "web"},
		MigrateVolumes: []model.VolumeMigration{{Name: "root", StorageClass: sc}},
	})
	want := map[string]bool{
		"Power":             false, // the power cycle itself
		"CPU":               true,
		"Label tier":        false,
		"Disk root storage": false, // storage migration is live
	}
	seen := map[string]bool{}
	for _, c := range changes {
		seen[c.Field] = true
		expect, ok := want[c.Field]
		if !ok {
			continue
		}
		if c.Restart != expect {
			t.Errorf("%s: Restart=%v, want %v", c.Field, c.Restart, expect)
		}
	}
	for f := range want {
		if !seen[f] {
			t.Errorf("expected a change for %s", f)
		}
	}
}
