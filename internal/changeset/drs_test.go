package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/drsgen"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// drsFiles renders spec's file set keyed by path, for seeding repos.
func drsFiles(t *testing.T, spec drsgen.Spec) map[string][]byte {
	t.Helper()
	files, err := drsgen.Manifests(spec)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range files {
		out[f.Path] = f.Content
	}
	return out
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestStageEnableDRSStagesFileSet(t *testing.T) {
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	view, err := c.StageEnableDRS(id, proj, mustJSON(t, drsgen.Spec{Mode: drsgen.ModeAutomatic, InstallPSI: true}))
	if err != nil {
		t.Fatalf("StageEnableDRS: %v", err)
	}
	if view.Count != 5 {
		t.Fatalf("want 5 staged items (install + CR + PSI), got %d", view.Count)
	}
	for _, it := range view.Items {
		if it.Resource != string(draft.ResourceDRS) || it.Kind != string(draft.KindCreate) {
			t.Fatalf("unexpected item: %+v", it)
		}
	}
}

func TestStageEnableDRSStagesOnlyChangedFiles(t *testing.T) {
	// The operator install (+ a Predictive CR) is already committed; switching to
	// Automatic must stage only the KubeDescheduler CR.
	bare := seedBareFiles(t, drsFiles(t, drsgen.Spec{Mode: drsgen.ModePredictive}))
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	view, err := c.StageEnableDRS(id, proj, mustJSON(t, drsgen.Spec{Mode: drsgen.ModeAutomatic}))
	if err != nil {
		t.Fatalf("StageEnableDRS: %v", err)
	}
	if view.Count != 1 || view.Items[0].Name != "kubedescheduler" {
		t.Fatalf("want only the kubedescheduler entry, got %+v", view.Items)
	}
}

func TestStageEnableDRSUnchangedIsCleanDraft(t *testing.T) {
	// Declarative: a spec matching the base branch is an empty delta — a clean
	// draft, not an error. With a pending change staged first, the same submit
	// is the cancel gesture (revert to the committed configuration).
	spec := drsgen.Spec{Mode: drsgen.ModeAutomatic}
	bare := seedBareFiles(t, drsFiles(t, spec))
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	view, err := c.StageEnableDRS(id, proj, mustJSON(t, spec))
	if err != nil {
		t.Fatalf("StageEnableDRS unchanged: %v", err)
	}
	if view.Count != 0 {
		t.Fatalf("want empty draft for an unchanged config, got %d items", view.Count)
	}

	if _, err := c.StageEnableDRS(id, proj, mustJSON(t, drsgen.Spec{Mode: drsgen.ModePredictive, InstallPSI: true})); err != nil {
		t.Fatal(err)
	}
	view, err = c.StageEnableDRS(id, proj, mustJSON(t, spec))
	if err != nil {
		t.Fatalf("StageEnableDRS revert-to-committed: %v", err)
	}
	if view.Count != 0 {
		t.Fatalf("want the pending change cancelled, got %d items", view.Count)
	}
}

func TestStageEnableDRSReconfigureDropsStaleSiblings(t *testing.T) {
	// A re-configure without the PSI opt-in must not leave the previously
	// staged MachineConfig behind — the draft is replaced wholesale.
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	if _, err := c.StageEnableDRS(id, proj, mustJSON(t, drsgen.Spec{Mode: drsgen.ModeAutomatic, InstallPSI: true})); err != nil {
		t.Fatal(err)
	}
	view, err := c.StageEnableDRS(id, proj, mustJSON(t, drsgen.Spec{Mode: drsgen.ModeAutomatic}))
	if err != nil {
		t.Fatal(err)
	}
	if view.Count != 4 {
		t.Fatalf("want 4 items after dropping PSI, got %d", view.Count)
	}
	for _, it := range view.Items {
		if it.Name == "psi-machineconfig" {
			t.Fatal("stale PSI entry survived the re-configure")
		}
	}
}

func TestDRSDraft(t *testing.T) {
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	state, err := c.DRSDraft(id, proj)
	if err != nil {
		t.Fatal(err)
	}
	if state.Config != nil || state.PSI || state.DisableStaged {
		t.Fatalf("want empty draft state, got %+v", state)
	}

	soft := false
	spec := drsgen.Spec{Mode: drsgen.ModePredictive, Threshold: "High", IntervalSeconds: 120,
		SoftTainter: &soft, InstallPSI: true}
	if _, err := c.StageEnableDRS(id, proj, mustJSON(t, spec)); err != nil {
		t.Fatal(err)
	}
	state, err = c.DRSDraft(id, proj)
	if err != nil {
		t.Fatal(err)
	}
	if state.Config == nil || !state.PSI || state.DisableStaged {
		t.Fatalf("want staged config + PSI, got %+v", state)
	}
	if state.Config.Mode != "Predictive" || state.Config.Threshold != "High" ||
		state.Config.IntervalSeconds != 120 || state.Config.SoftTainter {
		t.Fatalf("staged config doesn't round-trip: %+v", state.Config)
	}
}

func TestUnstageDRSIsAtomic(t *testing.T) {
	// The DRS file set is one logical change: unstaging any entry (the
	// ChangesPanel's per-row button) must drop the whole set, never leaving a
	// proposable half-install.
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	if _, err := c.StageEnableDRS(id, proj, mustJSON(t, drsgen.Spec{Mode: drsgen.ModeAutomatic, InstallPSI: true})); err != nil {
		t.Fatal(err)
	}
	if err := c.Unstage(id, proj, string(draft.ResourceDRS), ClusterScopeNS, "subscription"); err != nil {
		t.Fatalf("Unstage: %v", err)
	}
	view, err := c.Get(id, proj)
	if err != nil {
		t.Fatal(err)
	}
	if view.Count != 0 {
		t.Fatalf("want the whole DRS set unstaged, got %d items: %+v", view.Count, view.Items)
	}
}

func TestStageDisableDRSStagesRemoval(t *testing.T) {
	bare := seedBareFiles(t, drsFiles(t, drsgen.Spec{Mode: drsgen.ModeAutomatic}))
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	view, err := c.StageDisableDRS(id, proj)
	if err != nil {
		t.Fatalf("StageDisableDRS: %v", err)
	}
	if view.Count != 1 {
		t.Fatalf("want 1 staged item, got %d", view.Count)
	}
	it := view.Items[0]
	if it.Kind != string(draft.KindDelete) || it.Name != "kubedescheduler" {
		t.Fatalf("unexpected item: %+v", it)
	}
}

func TestStageDisableDRSNotConfigured(t *testing.T) {
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	if _, err := c.StageDisableDRS(id, proj); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("want model.ErrNotFound, got %v", err)
	}
}

func TestStageDisableDRSCancelsPendingEnable(t *testing.T) {
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	if _, err := c.StageEnableDRS(id, proj, mustJSON(t, drsgen.Spec{Mode: drsgen.ModeAutomatic})); err != nil {
		t.Fatal(err)
	}
	view, err := c.StageDisableDRS(id, proj)
	if err != nil {
		t.Fatalf("StageDisableDRS: %v", err)
	}
	if view.Count != 0 {
		t.Fatalf("want the pending enable cancelled (empty draft), got %d items", view.Count)
	}
}

// The full draft → branch → remote flow: an enable proposes exactly the drsgen
// file set onto the pushed working branch, and the content round-trips through
// Parse. Push is enabled (unlike newProposeFixture) so the bare remote is the
// thing asserted on — what the forge PR would actually contain.
func TestProposeDRSCommitsFileSet(t *testing.T) {
	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := git.NewRepoSet(ctx, "", nil, true, nil, time.Hour)
	c := New(store, repos, nil, nil, "main", "dotvirt/proposed", "running")
	id := auth.Identity{Username: "admin"}
	bare := seedBareFiles(t, nil)
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	spec := drsgen.Spec{Mode: drsgen.ModeAutomatic, Threshold: "Medium", InstallPSI: true}
	if _, err := c.StageEnableDRS(id, proj, mustJSON(t, spec)); err != nil {
		t.Fatalf("StageEnableDRS: %v", err)
	}
	out, err := c.Propose(id, proj, model.ProposeRequest{Title: "enable DRS"})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if !out.Pushed {
		t.Fatalf("branch not pushed: %+v", out)
	}

	show := func(path string) []byte {
		t.Helper()
		cmd := exec.Command("git", "show", out.Branch+":"+path)
		cmd.Dir = bare
		got, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s missing on pushed %s: %v", path, out.Branch, err)
		}
		return got
	}
	files, err := drsgen.Manifests(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 5 {
		t.Fatalf("want 5 files, got %d", len(files))
	}
	for _, want := range files {
		if string(show(want.Path)) != string(want.Content) {
			t.Errorf("%s content mismatch on pushed branch", want.Path)
		}
	}
	parsed, err := drsgen.Parse(show(drsgen.CRPath))
	if err != nil || parsed.Mode != "Automatic" || parsed.Threshold != "Medium" {
		t.Fatalf("committed CR doesn't round-trip: %+v (%v)", parsed, err)
	}
}

func TestDRSState(t *testing.T) {
	c := newTestCoordinator(t)

	empty := project.ProjectInfo{Name: "platform", Repo: seedBareFiles(t, nil)}
	state, err := c.DRSState(empty)
	if err != nil {
		t.Fatal(err)
	}
	if state.Configured || state.PSIConfigured || state.Config != nil {
		t.Fatalf("want unconfigured state, got %+v", state)
	}

	soft := false
	spec := drsgen.Spec{Mode: drsgen.ModePredictive, Threshold: "Medium", IntervalSeconds: 120,
		SoftTainter: &soft, EvictionNodeLimit: 3, EvictionTotalLimit: 7, InstallPSI: true}
	configured := project.ProjectInfo{Name: "platform", Repo: seedBareFiles(t, drsFiles(t, spec))}
	state, err = c.DRSState(configured)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Configured || !state.PSIConfigured || state.Config == nil {
		t.Fatalf("want configured state with PSI, got %+v", state)
	}
	cfg := *state.Config
	if cfg.Mode != "Predictive" || cfg.Threshold != "Medium" || cfg.IntervalSeconds != 120 ||
		cfg.SoftTainter || cfg.EvictionNodeLimit != 3 || cfg.EvictionTotalLimit != 7 {
		t.Fatalf("unexpected parsed config: %+v", cfg)
	}
}
