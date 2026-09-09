package changeset

import (
	"errors"
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
	c := newTestCoordinator(t)
	proj := project.ProjectInfo{Name: "p", Repo: bare}
	id := auth.Identity{Username: "admin"}

	commits, err := c.History(proj, 25)
	if err != nil || len(commits) != 2 {
		t.Fatalf("History = %v %+v", err, commits)
	}
	initial := commits[1].Hash

	view, err := c.RestoreVersion(id, proj, "alpha", "web", initial)
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

	if _, err := c.RestoreVersion(id, proj, "alpha", "web", mergeHash); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("restoring the current version should be ErrInvalid, got %v", err)
	}
	if _, err := c.RestoreVersion(id, proj, "alpha", "db", initial); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("a file absent at that commit should be ErrNotFound, got %v", err)
	}
	if _, err := c.RestoreVersion(id, proj, "alpha", "ghost", initial); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("an untracked VM should be ErrNotFound, got %v", err)
	}
}

// A namespace's history is the commits that touched its directory.
func TestNamespaceHistoryFollowsDirectory(t *testing.T) {
	bare, _, mergeHash := seedMerged(t)
	c := newTestCoordinator(t)
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
