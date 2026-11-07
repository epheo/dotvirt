package changeset

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
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
