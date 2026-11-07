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
