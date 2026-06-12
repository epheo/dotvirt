package inventory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// seedRepo makes a bare repo on main holding two VMs in different namespaces.
func seedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	run := func(wd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wd
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// -b main on the bare init too: without it HEAD points at the host git's
	// default branch (often an unborn master), and go-git's clone fails
	// "reference not found" on machines without init.defaultBranch=main.
	run(dir, "init", "-q", "--bare", "-b", "main", bare)
	run(dir, "init", "-q", "-b", "main", work)

	write := func(path, content string) {
		full := filepath.Join(work, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tenant-a/web.yaml", vmYAML("web", "tenant-a"))
	write("tenant-b/db.yaml", vmYAML("db", "tenant-b"))
	run(work, "add", "-A")
	run(work, "commit", "-qm", "seed")
	run(work, "remote", "add", "origin", bare)
	run(work, "push", "-q", "origin", "main")
	return bare
}

func vmYAML(name, ns string) string {
	return `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: ` + name + `
  namespace: ` + ns + `
spec:
  runStrategy: Always
  template:
    spec:
      domain:
        memory:
          guest: 1Gi
`
}

func TestBuildFiltersAndEnriches(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repos := git.NewRepoSet(ctx, "", "", false, make(chan struct{}, 1), nil, time.Hour)

	in := Inputs{
		Branch: "main",
		Repos:  repos,
		Projects: []project.ProjectInfo{
			{Name: "team-a", Repo: bare, Namespaces: []string{"tenant-a"}},
			{Name: "broken", Error: "no repo configured"}, // unresolved: listed, empty
		},
		Live: map[string]clusterstate.LiveVM{
			"tenant-a/web": {Phase: "Running", GuestIP: "10.0.0.1"},
		},
	}

	inv := Build(in)
	if len(inv.Projects) != 2 {
		t.Fatalf("want 2 projects, got %d", len(inv.Projects))
	}

	teamA := inv.Projects[0]
	if teamA.Name != "team-a" || teamA.Repo != bare {
		t.Fatalf("team-a wrong: %+v", teamA)
	}
	if len(teamA.Namespaces) != 1 || teamA.Namespaces[0].Namespace != "tenant-a" {
		t.Fatalf("team-a namespaces wrong: %+v", teamA.Namespaces)
	}
	vms := teamA.Namespaces[0].VMs
	if len(vms) != 1 || vms[0].Name != "web" {
		t.Fatalf("team-a should hold only its own VM (web), got %+v", vms)
	}
	// tenant-b/db must NOT leak into team-a (filtered by namespace).
	if vms[0].Namespace != "tenant-a" {
		t.Error("a VM from outside the project's namespaces leaked in")
	}
	// Enrichment applied.
	if vms[0].Phase != "Running" || vms[0].GuestIP != "10.0.0.1" {
		t.Errorf("live state not merged: %+v", vms[0])
	}
	// Drift not enabled → Sync stays unset (not NotTracked).
	if vms[0].Sync == model.SyncNotTracked {
		t.Error("Sync should be unset when drift is disabled")
	}

	broken := inv.Projects[1]
	if broken.Error == "" || len(broken.Namespaces) != 0 {
		t.Errorf("broken project should keep its error and no namespaces: %+v", broken)
	}
}

// A VM present in the live snapshot but absent from git (a fresh clone target,
// an out-of-band create) is surfaced as a NotTracked row — empty SourceFile,
// Power Unknown — so "Adopt into git" has something to act on. VMs outside the
// project's namespaces stay invisible.
func TestBuildSurfacesClusterOnlyVMs(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repos := git.NewRepoSet(ctx, "", "", false, make(chan struct{}, 1), nil, time.Hour)

	in := Inputs{
		Branch:   "main",
		Repos:    repos,
		Projects: []project.ProjectInfo{{Name: "team-a", Repo: bare, Namespaces: []string{"tenant-a"}}},
		Live: map[string]clusterstate.LiveVM{
			"tenant-a/web":       {Phase: "Running"},
			"tenant-a/web-clone": {},                 // cluster-only: halted clone target
			"tenant-b/other":     {Phase: "Running"}, // outside the project → ignored
		},
	}
	inv := Build(in)
	vms := inv.Projects[0].Namespaces[0].VMs
	if len(vms) != 2 || vms[0].Name != "web" || vms[1].Name != "web-clone" {
		t.Fatalf("want [web web-clone], got %+v", vms)
	}
	clone := vms[1]
	if clone.Sync != model.SyncNotTracked || clone.SourceFile != "" || clone.Power != model.PowerUnknown {
		t.Errorf("cluster-only VM should be NotTracked with no source file and Unknown power, got %+v", clone)
	}
}

func TestBuildDriftEnabledMarksNotTracked(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repos := git.NewRepoSet(ctx, "", "", false, make(chan struct{}, 1), nil, time.Hour)

	in := Inputs{
		Branch: "main",
		Repos:  repos,
		// Non-nil (but empty) Drift ⇒ Argo is wired; no entry for web ⇒ NotTracked.
		Drift:    map[string]argo.Drift{},
		Projects: []project.ProjectInfo{{Name: "team-a", Repo: bare, Namespaces: []string{"tenant-a"}}},
	}
	inv := Build(in)
	vm := inv.Projects[0].Namespaces[0].VMs[0]
	if vm.Sync != model.SyncNotTracked {
		t.Errorf("with drift enabled and no Application, Sync should be NotTracked, got %q", vm.Sync)
	}
}
