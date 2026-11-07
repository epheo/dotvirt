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

// Restoring a version stages the file exactly as that commit held it, named
// by the version and explained by the fields it moves; the current version
// and a file the commit lacked are refused.
func TestRestoreVersionStagesPastManifest(t *testing.T) {
	bare, _, mergeHash := seedMerged(t)
	c := newTestCoordinator(t, false)
	proj := project.ProjectInfo{Name: "p", Repo: bare}
	id := auth.Identity{Username: "admin"}

	commits, err := c.History(proj, 25)
	if err != nil || len(commits) != 2 {
		t.Fatalf("History = %v %+v", err, commits)
	}
	initial := commits[1].Hash

	view, err := c.RestoreVersion(id, proj, "", "alpha", "web", initial)
	if err != nil {
		t.Fatalf("RestoreVersion: %v", err)
	}
	if len(view.Items) != 1 || view.Items[0].Kind != "edit" || view.Items[0].Name != "web" {
		t.Fatalf("draft = %+v", view.Items)
	}
	it := view.Items[0]
	if it.Changes[0].Field != "Restore" || it.Changes[0].To != "version "+initial[:8] {
		t.Errorf("first change should name the version, got %+v", it.Changes[0])
	}
	var cpu bool
	for _, ch := range it.Changes[1:] {
		if ch.Field == "CPU" && strings.HasPrefix(ch.From, "4") && strings.HasPrefix(ch.To, "2") {
			cpu = true
		}
	}
	if !cpu {
		t.Errorf("restore should list the CPU move 4 -> 2, got %+v", it.Changes)
	}
	if string(it.YAML) != string(sizedVM("web", 2)) || it.BaseYAML != string(sizedVM("web", 4)) {
		t.Errorf("restore should carry the old manifest verbatim over the current one")
	}

	if _, err := c.RestoreVersion(id, proj, "", "alpha", "web", mergeHash); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("restoring the current version should be ErrInvalid, got %v", err)
	}
	if _, err := c.RestoreVersion(id, proj, "", "alpha", "db", initial); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("a file absent at that commit should be ErrNotFound, got %v", err)
	}
	if _, err := c.RestoreVersion(id, proj, "", "alpha", "ghost", initial); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("an untracked VM should be ErrNotFound, got %v", err)
	}
}

// A namespace's history is the commits that touched its directory.
func TestNamespaceHistoryFollowsDirectory(t *testing.T) {
	bare, _, mergeHash := seedMerged(t)
	c := newTestCoordinator(t, false)
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	alpha, err := c.NamespaceHistory(proj, "alpha", 25)
	if err != nil || len(alpha) != 2 || alpha[0].Hash != mergeHash {
		t.Fatalf("alpha history = %v %+v", err, alpha)
	}
	beta, err := c.NamespaceHistory(proj, "beta", 25)
	if err != nil || len(beta) != 0 {
		t.Errorf("beta history = %v %+v, want none", err, beta)
	}
}

// A whole-file edit is content computed against base as it stood at staging.
// Once a later merge moves that file, proposing it would silently undo the
// merge: propose refuses, names the object, pushes nothing, keeps the draft.
func TestProposeRefusesWholeFileEditOverAMovedBase(t *testing.T) {
	bare, work := seedWork(t, map[string][]byte{"alpha/web.yaml": sizedVM("web", 2)})
	initial, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	commitWeb := func(cores int) {
		writeWorkFile(t, work, "alpha/web.yaml", sizedVM("web", cores))
		gitRun(t, work, "commit", "-qam", "resize")
		gitRun(t, work, "push", "-q", "origin", "main")
	}
	commitWeb(4)
	c := newTestCoordinator(t, true)
	proj := project.ProjectInfo{Name: "p", Repo: bare}
	id := auth.Identity{Username: "admin"}
	if _, err := c.RestoreVersion(id, proj, "", "alpha", "web", strings.TrimSpace(string(initial))); err != nil {
		t.Fatalf("RestoreVersion: %v", err)
	}

	commitWeb(6) // merged after the restore was staged
	_, err = c.Propose(id, proj, model.ProposeRequest{Title: "restore web"})
	if !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "alpha/web") {
		t.Fatalf("want ErrConflict naming alpha/web, got %v", err)
	}
	if out, err := exec.Command("git", "-C", bare, "branch", "--list", "dotvirt/*").Output(); err != nil || len(out) != 0 {
		t.Fatalf("nothing may be pushed, got branches %q (%v)", out, err)
	}
	if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 1 {
		t.Fatalf("the draft must stay for the user to redo, got %+v", entries)
	}
}

// A whole-file edit would silently drop the field edits staged for the same
// VM; it is refused with the way forward, and the field edits stay.
func TestWholeFileEditOverStagedFieldEditConflicts(t *testing.T) {
	bare, _, _ := seedMerged(t)
	c := newTestCoordinator(t, false)
	proj := project.ProjectInfo{Name: "p", Repo: bare}
	id := auth.Identity{Username: "admin"}
	commits, err := c.History(proj, 25)
	if err != nil || len(commits) != 2 {
		t.Fatalf("History = %v %+v", err, commits)
	}
	off := "Off"
	if _, err := c.StageEdit(id, proj, "alpha", "web", model.EditRequest{SourceFile: "alpha/web.yaml", VMEdit: model.VMEdit{Power: &off}}); err != nil {
		t.Fatalf("StageEdit: %v", err)
	}
	_, err = c.RestoreVersion(id, proj, "", "alpha", "web", commits[1].Hash)
	if !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "unstage") {
		t.Fatalf("want ErrConflict naming the way forward, got %v", err)
	}
	if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 1 || entries[0].Edit == nil {
		t.Fatalf("the staged field edit must stay, got %+v", entries)
	}
}
