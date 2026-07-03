package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/drsgen"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// seedBareFiles creates a bare repo with the given files on main — a platform
// repo in whatever DRS state a test needs.
func seedBareFiles(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	run := func(wd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run(dir, "init", "-q", "--bare", "-b", "main", bare)
	run(dir, "init", "-q", "-b", "main", work)
	if len(files) == 0 {
		files = map[string][]byte{"README.md": []byte("platform\n")}
	}
	for path, content := range files {
		full := filepath.Join(work, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(work, "add", "-A")
	run(work, "commit", "-qm", "seed")
	run(work, "remote", "add", "origin", bare)
	run(work, "push", "-q", "origin", "main")
	return bare
}

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

func TestStageEnableDRSUnchangedRejected(t *testing.T) {
	spec := drsgen.Spec{Mode: drsgen.ModeAutomatic}
	bare := seedBareFiles(t, drsFiles(t, spec))
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	if _, err := c.StageEnableDRS(id, proj, mustJSON(t, spec)); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("want model.ErrInvalid for an unchanged config, got %v", err)
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
