package changeset

import (
	"errors"
	"reflect"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

const labeledVM = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: web
  namespace: alpha
  labels:
    env: dev
spec:
  runStrategy: Always
  template:
    spec:
      domain:
        cpu:
          cores: 2
        memory:
          guest: 2Gi
`

func ptr[T any](v T) *T { return &v }

// stagedEdit is the one VM edit (id, proj)'s draft holds, nil when none.
func stagedEdit(t *testing.T, c *Coordinator, id auth.Identity, proj project.ProjectInfo) *model.VMEdit {
	t.Helper()
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	switch len(entries) {
	case 0:
		return nil
	case 1:
		return entries[0].Edit
	}
	t.Fatalf("want at most one entry, got %+v", entries)
	return nil
}

// A partial edit (the power button, bulk power, a storage move) adds to what
// is staged for the VM; it never drops the staged sizing.
func TestStageEditAddsToStagedEdit(t *testing.T) {
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: seedBareFiles(t, map[string][]byte{"web.yaml": []byte(labeledVM)})}
	stage := func(e model.VMEdit) {
		t.Helper()
		if _, err := c.StageEdit(id, proj, "alpha", "web", model.EditRequest{SourceFile: "web.yaml", VMEdit: e}); err != nil {
			t.Fatalf("StageEdit: %v", err)
		}
	}

	stage(model.VMEdit{CPUCores: ptr(4), SetLabels: map[string]string{"tier": "gold"}})
	stage(model.VMEdit{Power: ptr("Off")})
	got := stagedEdit(t, c, id, proj)
	if got == nil || got.CPUCores == nil || *got.CPUCores != 4 || got.Power == nil || *got.Power != "Off" || got.SetLabels["tier"] != "gold" {
		t.Fatalf("every staged field must survive a partial edit, got %+v", got)
	}

	// Staging git's own value unstages that field alone.
	stage(model.VMEdit{CPUCores: ptr(2)})
	if got := stagedEdit(t, c, id, proj); got.CPUCores != nil || got.Power == nil || got.SetLabels["tier"] != "gold" {
		t.Fatalf("cpu back at git's value must unstage it and keep the rest, got %+v", got)
	}
	stage(model.VMEdit{Power: ptr("On"), SetLabels: map[string]string{"env": "dev"}})
	if got := stagedEdit(t, c, id, proj); got.Power != nil || len(got.SetLabels) != 1 {
		t.Fatalf("want only the tier label left, got %+v", got)
	}
	// Removing a label only the draft adds leaves nothing to change.
	stage(model.VMEdit{RemoveLabels: []string{"tier"}})
	if got := stagedEdit(t, c, id, proj); got != nil {
		t.Fatalf("want the VM unstaged, got %+v", got)
	}
}

// Back at git's value everywhere, the VM has nothing staged.
func TestStageEditBackToGitUnstagesVM(t *testing.T) {
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: seedBareFiles(t, map[string][]byte{"web.yaml": []byte(labeledVM)})}
	req := func(e model.VMEdit) model.EditRequest { return model.EditRequest{SourceFile: "web.yaml", VMEdit: e} }

	if _, err := c.StageEdit(id, proj, "alpha", "web", req(model.VMEdit{Memory: ptr("8Gi")})); err != nil {
		t.Fatal(err)
	}
	view, err := c.StageEdit(id, proj, "alpha", "web", req(model.VMEdit{Memory: ptr("2Gi")}))
	if err != nil {
		t.Fatal(err)
	}
	if view.Count != 0 || stagedEdit(t, c, id, proj) != nil {
		t.Fatalf("want an empty draft, got %+v", view)
	}
}

// A field edit cannot extend a staged create, delete or whole-file edit: it
// is refused, and what is staged stays.
func TestStageEditOntoOtherChangeConflicts(t *testing.T) {
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t)}
	if _, err := c.StageDelete(id, proj, "", "alpha", "web"); err != nil {
		t.Fatal(err)
	}
	_, err := c.StageEdit(id, proj, "alpha", "web", model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{Power: ptr("Off")}})
	if !errors.Is(err, model.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	entries, _ := c.store.List(id.Username, proj.Name)
	if len(entries) != 1 || entries[0].Kind != "delete" {
		t.Fatalf("the staged delete must stay, got %+v", entries)
	}
}

func TestOverlayEdit(t *testing.T) {
	cases := []struct {
		name         string
		staged, next model.VMEdit
		want         model.VMEdit
	}{
		{"scalars: next wins, the rest stays",
			model.VMEdit{Power: ptr("Off"), CPUCores: ptr(4)},
			model.VMEdit{Power: ptr("On")},
			model.VMEdit{Power: ptr("On"), CPUCores: ptr(4)}},
		{"labels merge; a set cancels a staged removal and the reverse",
			model.VMEdit{SetLabels: map[string]string{"a": "1", "b": "1"}, RemoveLabels: []string{"c"}},
			model.VMEdit{SetLabels: map[string]string{"c": "2"}, RemoveLabels: []string{"b"}},
			model.VMEdit{SetLabels: map[string]string{"a": "1", "c": "2"}, RemoveLabels: []string{"b"}}},
		{"devices merge by name; an add and a removal of one device net to nothing",
			model.VMEdit{AddDisks: []model.DiskAdd{{Name: "d1", Size: "1Gi"}, {Name: "d2", Size: "1Gi"}}, RemoveNetworks: []string{"n1"}},
			model.VMEdit{AddDisks: []model.DiskAdd{{Name: "d1", Size: "5Gi"}}, RemoveDisks: []string{"d2", "d3"}, AddNetworks: []model.NetworkAdd{{Name: "n1"}}},
			model.VMEdit{AddDisks: []model.DiskAdd{{Name: "d1", Size: "5Gi"}}, RemoveDisks: []string{"d3"}}},
		{"groups and storage moves upsert by name",
			model.VMEdit{AddGroups: []model.PlacementGroup{{Name: "g", Mode: "apart"}}, MigrateVolumes: []model.VolumeMigration{{Name: "root", StorageClass: "a"}}},
			model.VMEdit{AddGroups: []model.PlacementGroup{{Name: "g", Mode: "together"}}, MigrateVolumes: []model.VolumeMigration{{Name: "root", StorageClass: "b"}}},
			model.VMEdit{AddGroups: []model.PlacementGroup{{Name: "g", Mode: "together"}}, MigrateVolumes: []model.VolumeMigration{{Name: "root", StorageClass: "b"}}}},
		{"an inline size replaces a staged instance type",
			model.VMEdit{Sizing: ptr("instancetype"), Instancetype: ptr("u1.large"), Power: ptr("Off")},
			model.VMEdit{CPUCores: ptr(4)},
			model.VMEdit{CPUCores: ptr(4), Power: ptr("Off")}},
		{"an instance type replaces staged inline sizing",
			model.VMEdit{CPUCores: ptr(4), Memory: ptr("8Gi")},
			model.VMEdit{Sizing: ptr("instancetype"), Instancetype: ptr("u1.large")},
			model.VMEdit{Sizing: ptr("instancetype"), Instancetype: ptr("u1.large")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := overlayEdit(c.staged, c.next)
			if !reflect.DeepEqual(normalized(got), normalized(c.want)) {
				t.Fatalf("got  %+v\nwant %+v", got, c.want)
			}
		})
	}
}

// normalized reads empty lists as absent, the way the wire and Empty do.
func normalized(e model.VMEdit) model.VMEdit {
	if len(e.SetLabels) == 0 {
		e.SetLabels = nil
	}
	for _, l := range []*[]string{&e.RemoveLabels, &e.RemoveDisks, &e.RemoveNetworks, &e.RemoveGroups} {
		if len(*l) == 0 {
			*l = nil
		}
	}
	if len(e.AddDisks) == 0 {
		e.AddDisks = nil
	}
	if len(e.AddNetworks) == 0 {
		e.AddNetworks = nil
	}
	if len(e.AddGroups) == 0 {
		e.AddGroups = nil
	}
	if len(e.MigrateVolumes) == 0 {
		e.MigrateVolumes = nil
	}
	return e
}

func TestWithoutBase(t *testing.T) {
	vm := model.VM{
		Power: model.PowerOn, CPUCores: 2, Memory: "2Gi", Labels: map[string]string{"env": "dev"},
		Scheduling: &model.VMScheduling{Pin: []string{"n1"}, Groups: []model.PlacementGroup{{Name: "g", Mode: "apart"}}},
	}
	got := withoutBase(model.VMEdit{
		Power: ptr("On"), Sizing: ptr("custom"), CPUCores: ptr(2), Memory: ptr("4Gi"),
		SetLabels: map[string]string{"env": "dev", "tier": "gold"}, RemoveLabels: []string{"ghost"},
		Pin: &[]string{"n1"}, AddGroups: []model.PlacementGroup{{Name: "g", Mode: "apart"}}, RemoveGroups: []string{"ghost"},
		RemoveDisks: []string{"ghost"},
	}, vm)
	want := model.VMEdit{Memory: ptr("4Gi"), SetLabels: map[string]string{"tier": "gold"}, RemoveDisks: []string{"ghost"}}
	if !reflect.DeepEqual(normalized(got), normalized(want)) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}
