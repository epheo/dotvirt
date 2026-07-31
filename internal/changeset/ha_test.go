package changeset

import (
	"errors"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/hagen"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// haFiles renders spec's file set keyed by path, for seeding repos.
func haFiles(t *testing.T, spec hagen.Spec) map[string][]byte {
	t.Helper()
	files, err := hagen.Manifests(spec)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range files {
		out[f.Path] = f.Content
	}
	return out
}

func TestStageEnableHAStagesFileSet(t *testing.T) {
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	view, err := c.StageEnableHA(id, proj, mustJSON(t, hagen.Spec{}))
	if err != nil {
		t.Fatalf("StageEnableHA: %v", err)
	}
	if view.Count != 4 {
		t.Fatalf("want 4 staged items (install + CR), got %d", view.Count)
	}
	for _, it := range view.Items {
		if it.Resource != string(draft.ResourceHA) || it.Kind != string(draft.KindCreate) {
			t.Fatalf("unexpected item: %+v", it)
		}
	}
}

func TestStageEnableHAStagesOnlyChangedFiles(t *testing.T) {
	// The operator install (+ a default CR) is already committed; changing the
	// detection patience must stage only the NodeHealthCheck CR.
	bare := seedBareFiles(t, haFiles(t, hagen.Spec{}))
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	view, err := c.StageEnableHA(id, proj, mustJSON(t, hagen.Spec{UnhealthySeconds: 120}))
	if err != nil {
		t.Fatalf("StageEnableHA: %v", err)
	}
	if view.Count != 1 || view.Items[0].Name != "nodehealthcheck" {
		t.Fatalf("want only the nodehealthcheck entry, got %+v", view.Items)
	}
}

func TestStageEnableHAUnchangedIsCleanDraft(t *testing.T) {
	// Declarative: a spec matching the base branch is an empty delta - a clean
	// draft, not an error - and re-submitting the committed configuration is
	// the cancel gesture for a pending change.
	spec := hagen.Spec{UnhealthySeconds: 120}
	bare := seedBareFiles(t, haFiles(t, spec))
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	view, err := c.StageEnableHA(id, proj, mustJSON(t, spec))
	if err != nil {
		t.Fatalf("StageEnableHA unchanged: %v", err)
	}
	if view.Count != 0 {
		t.Fatalf("want empty draft for an unchanged config, got %d items", view.Count)
	}

	if _, err := c.StageEnableHA(id, proj, mustJSON(t, hagen.Spec{UnhealthySeconds: 600})); err != nil {
		t.Fatal(err)
	}
	view, err = c.StageEnableHA(id, proj, mustJSON(t, spec))
	if err != nil {
		t.Fatalf("StageEnableHA revert-to-committed: %v", err)
	}
	if view.Count != 0 {
		t.Fatalf("want the pending change cancelled, got %d items", view.Count)
	}
}

func TestUnstageHAIsAtomic(t *testing.T) {
	// The HA file set is one logical change: unstaging any entry must drop the
	// whole set, never leaving a proposable half-install.
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	if _, err := c.StageEnableHA(id, proj, mustJSON(t, hagen.Spec{})); err != nil {
		t.Fatal(err)
	}
	if err := c.Unstage(id, proj, string(draft.ResourceHA), ClusterScopeNS, "subscription"); err != nil {
		t.Fatalf("Unstage: %v", err)
	}
	view, err := c.Get(id, proj)
	if err != nil {
		t.Fatal(err)
	}
	if view.Count != 0 {
		t.Fatalf("want the whole HA set unstaged, got %d items: %+v", view.Count, view.Items)
	}
}

func TestStageDisableHAStagesRemoval(t *testing.T) {
	bare := seedBareFiles(t, haFiles(t, hagen.Spec{}))
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	view, err := c.StageDisableHA(id, proj)
	if err != nil {
		t.Fatalf("StageDisableHA: %v", err)
	}
	if view.Count != 1 {
		t.Fatalf("want 1 staged item, got %d", view.Count)
	}
	it := view.Items[0]
	if it.Kind != string(draft.KindDelete) || it.Name != "nodehealthcheck" {
		t.Fatalf("unexpected item: %+v", it)
	}
}

func TestStageDisableHANotConfigured(t *testing.T) {
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	if _, err := c.StageDisableHA(id, proj); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("want model.ErrNotFound, got %v", err)
	}
}

func TestStageDisableHACancelsPendingEnable(t *testing.T) {
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	if _, err := c.StageEnableHA(id, proj, mustJSON(t, hagen.Spec{})); err != nil {
		t.Fatal(err)
	}
	view, err := c.StageDisableHA(id, proj)
	if err != nil {
		t.Fatalf("StageDisableHA: %v", err)
	}
	if view.Count != 0 {
		t.Fatalf("want the pending enable cancelled (empty draft), got %d items", view.Count)
	}
}

func TestHAState(t *testing.T) {
	c := newTestCoordinator(t)

	empty := project.ProjectInfo{Name: "platform", Repo: seedBareFiles(t, nil)}
	state, err := c.HAState(empty)
	if err != nil {
		t.Fatal(err)
	}
	if state.Configured || state.Config != nil {
		t.Fatalf("want unconfigured state, got %+v", state)
	}

	spec := hagen.Spec{UnhealthySeconds: 120, MinHealthyPercent: 40}
	configured := project.ProjectInfo{Name: "platform", Repo: seedBareFiles(t, haFiles(t, spec))}
	state, err = c.HAState(configured)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Configured || state.Config == nil {
		t.Fatalf("want configured state, got %+v", state)
	}
	if state.Config.UnhealthySeconds != 120 || state.Config.MinHealthyPercent != 40 {
		t.Fatalf("unexpected parsed config: %+v", state.Config)
	}
}

func TestHADraft(t *testing.T) {
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	state, err := c.HADraft(id, proj)
	if err != nil {
		t.Fatal(err)
	}
	if state.Config != nil || state.DisableStaged {
		t.Fatalf("want empty draft state, got %+v", state)
	}

	if _, err := c.StageEnableHA(id, proj, mustJSON(t, hagen.Spec{UnhealthySeconds: 240, MinHealthyPercent: 30})); err != nil {
		t.Fatal(err)
	}
	state, err = c.HADraft(id, proj)
	if err != nil {
		t.Fatal(err)
	}
	if state.Config == nil || state.DisableStaged {
		t.Fatalf("want staged config, got %+v", state)
	}
	if state.Config.UnhealthySeconds != 240 || state.Config.MinHealthyPercent != 30 {
		t.Fatalf("staged config doesn't round-trip: %+v", state.Config)
	}
}
