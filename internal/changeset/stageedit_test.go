package changeset

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// nsLessVM is a VM manifest without metadata.namespace: it lives in whatever
// namespace its directory names.
const nsLessVM = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: x
spec:
  runStrategy: Always
`

// The file an edit rewrites comes from git, never from the client: a client
// naming a/x.yaml for an edit of b/x would otherwise land the change on a/x
// while the PR says b/x.
func TestStageEditResolvesSourceFileFromGit(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{"a/x.yaml": []byte(nsLessVM), "b/x.yaml": []byte(nsLessVM)})
	c := newTestCoordinator(t, true)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	req := model.EditRequest{SourceFile: "a/x.yaml", VMEdit: model.VMEdit{SetLabels: map[string]string{"env": "prod"}}}
	if _, err := c.StageEdit(id, proj, "b", "x", req); err != nil {
		t.Fatalf("StageEdit: %v", err)
	}
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil || len(entries) != 1 || entries[0].SourceFile != "b/x.yaml" {
		t.Fatalf("want the edit staged against b/x.yaml, got %+v (%v)", entries, err)
	}

	res, err := c.Propose(id, proj, model.ProposeRequest{Title: "label b/x"})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	for path, want := range map[string]bool{"a/x.yaml": false, "b/x.yaml": true} {
		got, err := exec.Command("git", "-C", bare, "cat-file", "-p", res.Branch+":"+path).Output()
		if err != nil {
			t.Fatalf("%s on %s: %v", path, res.Branch, err)
		}
		if strings.Contains(string(got), "env: prod") != want {
			t.Errorf("%s labeled = %v, want %v:\n%s", path, !want, want, got)
		}
	}
}

// An edit is dry-run against the file at stage, so a refusal lands on the form
// with its reason and nothing is staged that propose would later choke on.
func TestStageEditDryRuns(t *testing.T) {
	bare := seedBare(t) // alpha/web: no template, so no domain to size
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	cpu := 2
	for _, tc := range []struct {
		name string
		vm   string
		edit model.VMEdit
		kind error
	}{
		{"invalid label value", "web", model.VMEdit{SetLabels: map[string]string{"env": "a\n---\nkind: RoleBinding"}}, model.ErrInvalid},
		{"field the manifest cannot take", "web", model.VMEdit{CPUCores: &cpu}, model.ErrConflict},
		{"VM git does not declare", "ghost", model.VMEdit{CPUCores: &cpu}, model.ErrNotFound},
	} {
		_, err := c.StageEdit(id, proj, "alpha", tc.vm, model.EditRequest{SourceFile: "web.yaml", VMEdit: tc.edit})
		if !errors.Is(err, tc.kind) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.kind, err)
		}
	}
	if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 0 {
		t.Errorf("a refused edit must not be staged, got %+v", entries)
	}
}

// A staged edit the base branch has since moved out from under (the VM's file
// deleted or rewritten by another merge, or a draft staged before a check
// existed) fails the propose as a conflict naming the entry to unstage, never
// as an unclassified error.
func TestProposeEditThatNoLongerAppliesIsConflict(t *testing.T) {
	const other = "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: other\n  namespace: alpha\nspec:\n  runStrategy: Always\n"
	seed := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Always\n"
	label := model.VMEdit{SetLabels: map[string]string{"env": "prod"}}
	for _, tc := range []struct {
		name  string
		move  func(t *testing.T, work string)
		stage model.VMEdit
		named string
	}{
		{"file deleted", func(t *testing.T, work string) { gitRun(t, work, "rm", "-q", "web.yaml") }, label, "web.yaml"},
		{"VM gone from its file", func(t *testing.T, work string) {
			writeWorkFile(t, work, "web.yaml", []byte(other))
			gitRun(t, work, "add", "-A")
		}, label, "alpha/web"},
		{"draft from before validation", nil, model.VMEdit{SetLabels: map[string]string{"env": "has space"}}, "alpha/web"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bare, work := seedWork(t, map[string][]byte{"web.yaml": []byte(seed)})
			c := newTestCoordinator(t, true)
			id := auth.Identity{Username: "alice"}
			proj := project.ProjectInfo{Name: "p", Repo: bare}
			edit := tc.stage
			if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
				Kind: draft.KindEdit, Namespace: "alpha", Name: "web", SourceFile: "web.yaml", Edit: &edit,
			}); err != nil {
				t.Fatalf("Stage: %v", err)
			}
			if tc.move != nil {
				tc.move(t, work)
				gitRun(t, work, "commit", "-qm", "another merge")
				gitRun(t, work, "push", "-q", "origin", "main")
			}
			_, err := c.Propose(id, proj, model.ProposeRequest{Title: "label web"})
			if !errors.Is(err, model.ErrConflict) || errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), tc.named) {
				t.Fatalf("want ErrConflict naming %s, got %v", tc.named, err)
			}
			// The reason reads once, under the one kind the user sees.
			if msg := err.Error(); strings.Count(msg, model.ErrConflict.Error()+":") != 1 || strings.Contains(msg, model.ErrInvalid.Error()) {
				t.Errorf("want a single kind prefix, got %q", msg)
			}
			if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 1 {
				t.Errorf("a refused propose must keep the draft, got %+v", entries)
			}
		})
	}
}

// A preview refuses what staging would, so the inline editor shows a bad value
// before the user stages it.
func TestPreviewEditRefusesInvalidValues(t *testing.T) {
	c := newTestCoordinator(t, false)
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t)}
	bad := model.VMEdit{SetLabels: map[string]string{"env": "true\nkind: RoleBinding"}}
	if _, err := c.PreviewEdit(proj, "alpha", "web", bad); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

// flowMeta writes the VM's metadata as a flow mapping, which the editor cannot
// add labels to.
const flowMeta = "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata: {name: web, namespace: alpha}\nspec:\n  runStrategy: Always\n"

// The dry run covers the edit as it will be staged, merged with what the VM
// already has staged: a power toggle that applies on its own is still refused
// when the staged label it joins no longer can, and the draft keeps the
// staged edit as it was.
func TestStageEditChecksTheMergedEdit(t *testing.T) {
	f := newProposeFixture(t)
	label := model.EditRequest{VMEdit: model.VMEdit{SetLabels: map[string]string{"env": "prod"}}}
	if _, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", label); err != nil {
		t.Fatalf("StageEdit label: %v", err)
	}
	f.commitMain(t, "web.yaml", flowMeta)

	off := string(model.PowerOff)
	_, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", model.EditRequest{VMEdit: model.VMEdit{Power: &off}})
	if !errors.Is(err, model.ErrConflict) {
		t.Fatalf("want ErrConflict for the merged edit, got %v", err)
	}
	entries, err := f.c.store.List(f.id.Username, f.proj.Name)
	if err != nil || len(entries) != 1 || entries[0].Edit.Power != nil || entries[0].SourceFile != "web.yaml" {
		t.Fatalf("want the staged label edit untouched, got %+v (%v)", entries, err)
	}
}

// Adoption stages through the same check: drift the editor cannot write into
// the file is refused at adopt, not staged to fail the propose.
func TestAdoptChecksTheEdit(t *testing.T) {
	bare, _ := seedWork(t, map[string][]byte{"alpha/web.yaml": []byte(flowMeta)})
	c := newTestCoordinator(t, false)
	c.live = fakeLive{"alpha/web.yaml": liveVMYAML("web", "  labels:\n    env: prod\n")}
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	if _, err := c.Adopt(id, proj, "alpha", "web"); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 0 {
		t.Errorf("a refused adopt must not be staged, got %+v", entries)
	}
}

// Rebuilding an open PR replays the field edit it carries on the current
// main. One main no longer takes fails as a conflict naming the VM, and the
// way out is the PR, since unstaging the draft leaves the carried edit.
func TestReproposeOfAnEditMainNoLongerTakesIsConflict(t *testing.T) {
	f, _ := newPRFixture(t)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatalf("first Propose: %v", err)
	}
	f.commitMain(t, "web.yaml", flowMeta)
	off := string(model.PowerOff)
	if _, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", model.EditRequest{VMEdit: model.VMEdit{Power: &off}}); err != nil {
		t.Fatalf("StageEdit: %v", err)
	}
	_, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{})
	if !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "alpha/web") || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("want ErrConflict naming alpha/web and PR #1, got %v", err)
	}
	if n := f.draftCount(t); n != 1 {
		t.Errorf("a refused propose must keep the draft, got %d entries", n)
	}
}

// A VM two files declare has no one file an edit could rewrite: the declared
// index keeps the last, while the inventory may show the first.
func TestStageEditRefusesARepeatedDeclaration(t *testing.T) {
	a := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: x\nspec:\n  runStrategy: Always\n"
	b := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: x\n  namespace: a\nspec:\n  runStrategy: Always\n"
	bare := seedBareFiles(t, map[string][]byte{"a/x.yaml": []byte(a), "a/y.yaml": []byte(b)})
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}
	_, err := c.StageEdit(id, proj, "a", "x", model.EditRequest{VMEdit: model.VMEdit{SetLabels: map[string]string{"env": "prod"}}})
	if !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "a/x.yaml and a/y.yaml") {
		t.Fatalf("want ErrConflict naming both files, got %v", err)
	}
}

// Removals name interfaces, so a staged network add is cancelled by removing
// the interface it adds.
func TestStageEditRemovesAStagedNetworkByInterface(t *testing.T) {
	vm := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Always\n" +
		"  template:\n    spec:\n      domain:\n        devices:\n          interfaces:\n          - name: default\n            masquerade: {}\n" +
		"      networks:\n      - name: default\n        pod: {}\n"
	bare := seedBareFiles(t, map[string][]byte{"alpha/web.yaml": []byte(vm)})
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}
	add := model.VMEdit{AddNetworks: []model.NetworkAdd{{Name: "alpha/vlan-10"}}}
	if _, err := c.StageEdit(id, proj, "alpha", "web", model.EditRequest{VMEdit: add}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := c.StageEdit(id, proj, "alpha", "web", model.EditRequest{VMEdit: model.VMEdit{RemoveNetworks: []string{"vlan-10"}}}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 0 {
		t.Fatalf("the add and its removal must net to nothing, got %+v", entries)
	}
}
