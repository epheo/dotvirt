package inventory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// seedRepo makes a bare repo on main holding two VMs in different namespaces.
func seedRepo(t *testing.T) string {
	t.Helper()
	return seedRepoFiles(t, map[string]string{
		"tenant-a/web.yaml": vmYAML("web", "tenant-a"),
		"tenant-b/db.yaml":  vmYAML("db", "tenant-b"),
	})
}

// seedRepoFiles makes a bare repo on main holding files (path -> content).
func seedRepoFiles(t *testing.T, files map[string]string) string {
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
	for path, content := range files {
		write(path, content)
	}
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
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)

	in := Inputs{
		Branch: "main",
		Repos:  repos,
		Projects: []project.ProjectInfo{
			{Name: "team-a", Repo: bare, Namespaces: []string{"tenant-a"}},
			{Name: "broken", Error: "no repo configured"}, // unresolved: listed, empty
		},
		Live: clusterstate.LiveView{
			"tenant-a": {"web": {Phase: "Running", GuestIP: "10.0.0.1"}},
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
	// Drift not enabled -> Sync stays unset (not NotTracked).
	if vms[0].Sync == model.SyncNotTracked {
		t.Error("Sync should be unset when drift is disabled")
	}

	broken := inv.Projects[1]
	if broken.Error == "" || len(broken.Namespaces) != 0 {
		t.Errorf("broken project should keep its error and no namespaces: %+v", broken)
	}
}

// Documents Argo will not apply as dotvirt reads them ride the project as
// issues: one without metadata.namespace, one naming a namespace outside the
// project, one of a platform-tier kind. A caller sees only those whose
// namespace they can see or that belongs to no project: never one in a tenant
// namespace hidden from them.
func TestBuildSurfacesPlacementIssues(t *testing.T) {
	np := func(ns, name string) string {
		meta := "  name: " + name + "\n"
		if ns != "" {
			meta += "  namespace: " + ns + "\n"
		}
		return "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n" + meta
	}
	bare := seedRepoFiles(t, map[string]string{
		"a-dev/web.yaml":    vmYAML("web", "a-dev"),
		"a-dev/open.yaml":   np("", "open"),
		"a-prod/loose.yaml": np("", "loose"),
		"a-dev/into-b.yaml": np("b-ns", "into-b"),
		"a-dev/typo.yaml":   np("a-dve", "typo"),
		"a-dev/prod.yaml":   np("a-prod", "prod"),
		"a-dev/vlan.yaml":   "apiVersion: k8s.cni.cncf.io/v1\nkind: NetworkAttachmentDefinition\nmetadata:\n  name: vlan\n  namespace: a-dev\n",
		"a-prod/raw.yaml":   "apiVersion: k8s.cni.cncf.io/v1\nkind: NetworkAttachmentDefinition\nmetadata:\n  name: raw\n  namespace: a-prod\n",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	owners := map[string]string{"a-dev": "team-a", "a-prod": "team-a", "b-ns": "team-b"}
	build := func(visible map[string]bool) model.Project {
		return Build(Inputs{
			Branch: "main", Repos: repos, Visible: visible, Owners: owners,
			Projects: []project.ProjectInfo{{Name: "team-a", Repo: bare, Namespaces: []string{"a-dev"}}},
		}).Projects[0]
	}
	ref := func(ns, name string) model.ObjectRef {
		return model.ObjectRef{Kind: "NetworkPolicy", Namespace: ns, Name: name}
	}

	// Sees a-dev only: a-prod's loose document and B's namespace stay hidden;
	// the typo names no project's namespace, so it shows.
	got := build(map[string]bool{"a-dev": true})
	if !slices.Equal(got.Unnamespaced, []model.ObjectRef{ref("a-dev", "open")}) {
		t.Errorf("unnamespaced = %+v", got.Unnamespaced)
	}
	if !slices.Equal(got.OutOfProject, []model.ObjectRef{ref("a-dve", "typo")}) {
		t.Errorf("out of project = %+v", got.OutOfProject)
	}
	nad := func(ns, name string) model.ObjectRef {
		return model.ObjectRef{Kind: "NetworkAttachmentDefinition", Namespace: ns, Name: name}
	}
	if !slices.Equal(got.PlatformKinds, []model.ObjectRef{nad("a-dev", "vlan")}) {
		t.Errorf("platform kinds = %+v", got.PlatformKinds)
	}

	all := build(map[string]bool{"a-dev": true, "a-prod": true, "b-ns": true})
	if !slices.Equal(all.Unnamespaced, []model.ObjectRef{ref("a-dev", "open"), ref("a-prod", "loose")}) {
		t.Errorf("unnamespaced = %+v", all.Unnamespaced)
	}
	if !slices.Equal(all.OutOfProject, []model.ObjectRef{ref("a-dve", "typo"), ref("b-ns", "into-b")}) {
		t.Errorf("out of project = %+v", all.OutOfProject)
	}
	if !slices.Equal(all.PlatformKinds, []model.ObjectRef{nad("a-dev", "vlan"), nad("a-prod", "raw")}) {
		t.Errorf("platform kinds = %+v", all.PlatformKinds)
	}
}

// A running instance's address and host are theirs who may list instances.
func TestLiveFactsFollowTheInstances(t *testing.T) {
	live := clusterstate.LiveVM{Phase: "Running", GuestIP: "10.0.0.1", NodeName: "n1"}
	vm := model.VM{Namespace: "a"}
	applyLiveFor(&vm, live, Inputs{ListsVMIs: func(string) bool { return false }})
	if vm.Phase != "Running" || vm.GuestIP != "" || vm.NodeName != "" {
		t.Errorf("without instances: %+v", vm)
	}
	applyLiveFor(&vm, live, Inputs{})
	if vm.GuestIP != "10.0.0.1" || vm.NodeName != "n1" {
		t.Errorf("with instances: %+v", vm)
	}
}

// The rollup a project shows passes through the caller's view of it.
func TestBuildScopesProjectGitOps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := "https://forge.example/other.git"
	inv := Build(Inputs{
		Branch:       "main",
		Repos:        git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour),
		Projects:     []project.ProjectInfo{{Name: "team-b", Repo: repo, Namespaces: []string{"tenant-b"}}},
		ProjectDrift: map[string]model.ProjectSync{forge.NormalizeRepoURL(repo): {SyncError: "boom"}},
		ScopeSync: func(_ project.ProjectInfo, ps model.ProjectSync) *model.ProjectSync {
			ps.SyncError = "scoped"
			return &ps
		},
	})
	if got := inv.Projects[0].GitOps; got == nil || got.SyncError != "scoped" {
		t.Errorf("GitOps = %+v, want the scoped rollup", got)
	}
}

// The per-project GitOps rollup attaches to the project whose repo matches the
// managing Application, keyed by canonical repoURL - and a project with no matching
// Application (or when Argo is disabled) keeps GitOps nil, so nothing alarms.
func TestBuildAttachesProjectGitOps(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)

	in := Inputs{
		Branch: "main",
		Repos:  repos,
		Projects: []project.ProjectInfo{
			{Name: "team-a", Repo: bare, Namespaces: []string{"tenant-a"}},
			{Name: "team-b", Repo: "https://forge.example/other.git", Namespaces: []string{"tenant-b"}},
		},
		ProjectDrift: map[string]model.ProjectSync{
			forge.NormalizeRepoURL(bare): {Sync: model.SyncOutOfSync, Health: "Degraded", Operation: "Failed", SyncError: "boom"},
		},
	}
	inv := Build(in)

	if g := inv.Projects[0].GitOps; g == nil || g.Sync != model.SyncOutOfSync || g.Health != "Degraded" || g.SyncError != "boom" {
		t.Errorf("team-a GitOps not attached: %+v", g)
	}
	// team-b has no managing Application in the map -> GitOps stays nil (no false alarm).
	if g := inv.Projects[1].GitOps; g != nil {
		t.Errorf("team-b should have nil GitOps, got %+v", g)
	}
}

// A VM present in the live snapshot but absent from git (a fresh clone target,
// an out-of-band create) is surfaced as a NotTracked row - empty SourceFile,
// Power Unknown - so "Adopt into git" has something to act on. VMs outside the
// project's namespaces stay invisible.
func TestBuildSurfacesClusterOnlyVMs(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)

	in := Inputs{
		Branch:   "main",
		Repos:    repos,
		Projects: []project.ProjectInfo{{Name: "team-a", Repo: bare, Namespaces: []string{"tenant-a"}}},
		Live: clusterstate.LiveView{
			"tenant-a": {
				"web":       {Phase: "Running"},
				"web-clone": {}, // cluster-only: halted clone target
			},
			"tenant-b": {"other": {Phase: "Running"}}, // outside the project -> ignored
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

func TestBuildDriftEnabledMarksGitVMPending(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)

	in := Inputs{
		Branch: "main",
		Repos:  repos,
		// Non-nil (but empty) Drift => Argo is wired; no entry for a VM that IS
		// in git => Pending (awaiting its Application), never NotTracked.
		Drift:    map[string]argo.Drift{},
		Projects: []project.ProjectInfo{{Name: "team-a", Repo: bare, Namespaces: []string{"tenant-a"}}},
	}
	inv := Build(in)
	vm := inv.Projects[0].Namespaces[0].VMs[0]
	if vm.Sync != model.SyncPending {
		t.Errorf("with drift enabled and no Application, a git VM should be Pending, got %q", vm.Sync)
	}
}

// A labeled project with no resolvable repo yet (fresh adoption target, forge-less
// install) still renders its namespaces and running VMs as NotTracked - "not yet
// managed" must be visible, not blank.
func TestBuildRepoLessProjectShowsLiveVMs(t *testing.T) {
	in := Inputs{
		Branch: "main",
		Projects: []project.ProjectInfo{
			{Name: "team-a", Error: "no repo configured", Namespaces: []string{"tenant-a"}},
		},
		Live: clusterstate.LiveView{"tenant-a": {"web": {Phase: "Running"}}},
	}
	inv := Build(in)
	p := inv.Projects[0]
	if p.Error == "" {
		t.Fatal("the unresolved-repo error must stay on the project")
	}
	if len(p.Namespaces) != 1 || len(p.Namespaces[0].VMs) != 1 {
		t.Fatalf("live VM must render despite the missing repo, got %+v", p.Namespaces)
	}
	if vm := p.Namespaces[0].VMs[0]; vm.Name != "web" || vm.Sync != model.SyncNotTracked {
		t.Errorf("live VM should be NotTracked web, got %+v", vm)
	}
}

// Namespaces holding VMs but no project label are adoption candidates; labeled
// and system namespaces are not.
func TestAdoptable(t *testing.T) {
	live := clusterstate.LiveView{
		"legacy-a":             {"vm1": {Phase: "Running"}, "vm2": {}},
		"legacy-b":             {"vm1": {Phase: "Running"}},
		"tenant-a":             {"web": {Phase: "Running"}}, // labeled -> already a project
		"openshift-cnv":        {"plumbing": {}},            // system -> never proposed
		"acme-operator-system": {"op": {}},                  // an operator's home is never a tenant
		"kube-system":          {"should-skip": {}},
	}
	labeled := []project.Namespace{{Name: "tenant-a"}}
	got := Adoptable(live, labeled)
	want := []model.AdoptableNamespace{{Namespace: "legacy-a", VMs: 2}, {Namespace: "legacy-b", VMs: 1}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Adoptable = %+v, want %+v", got, want)
	}
}

// A dead repo must not blind the inventory: live VMs render (NotTracked) beside
// the error badge. Hiding them invited the wrong recovery actions.
func TestBuildRepoUnavailableStillShowsLiveVMs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)

	in := Inputs{
		Branch:   "main",
		Repos:    repos,
		Projects: []project.ProjectInfo{{Name: "team-a", Repo: "https://dead.example/x/y.git", Namespaces: []string{"tenant-a"}}},
		Live:     clusterstate.LiveView{"tenant-a": {"web": {Phase: "Running"}}},
	}
	inv := Build(in)
	p := inv.Projects[0]
	if p.Error == "" {
		t.Fatal("repo failure must surface as the project error")
	}
	if len(p.Namespaces) != 1 || len(p.Namespaces[0].VMs) != 1 {
		t.Fatalf("live VM must render despite the dead repo, got %+v", p.Namespaces)
	}
	if vm := p.Namespaces[0].VMs[0]; vm.Name != "web" || vm.Sync != model.SyncNotTracked {
		t.Errorf("live VM should be NotTracked web, got %+v", vm)
	}
}

// A repo that cannot be opened badges the project with a fixed message: the
// git error carries the repo path and transport detail.
func TestBuildRepoErrorHidesDetail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	missing := filepath.Join(t.TempDir(), "secret-path", "gone.git")
	in := Inputs{
		Branch:   "main",
		Repos:    git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour),
		Projects: []project.ProjectInfo{{Name: "team-a", Repo: missing, Namespaces: []string{"tenant-a"}}},
	}
	p := Build(in).Projects[0]
	if p.Error != "repo unavailable" {
		t.Errorf("project error = %q, want the fixed %q", p.Error, "repo unavailable")
	}
}

// A VM declared elsewhere in the repo is someone else's, even in a namespace
// the project holds.
func TestBuildReadsOnlyTheProjectsFolder(t *testing.T) {
	bare := seedRepoFiles(t, map[string]string{
		"clusters/a/tenant-a/web.yaml": vmYAML("web", "tenant-a"),
		"tenant-a/stray.yaml":          vmYAML("stray", "tenant-a"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	inv := Build(Inputs{
		Branch:   "main",
		Repos:    repos,
		Projects: []project.ProjectInfo{{Name: "team-a", Repo: bare, Path: "clusters/a", Namespaces: []string{"tenant-a"}}},
	})
	vms := inv.Projects[0].Namespaces[0].VMs
	if len(vms) != 1 || vms[0].Name != "web" {
		t.Errorf("vms = %+v, want the folder's web alone", vms)
	}
}

// The namespace stays in the tree; its VMs need their own right.
func TestBuildHidesVMsFromWhoMayNotListThem(t *testing.T) {
	bare := seedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	inv := Build(Inputs{
		Branch:   "main",
		Repos:    repos,
		Projects: []project.ProjectInfo{{Name: "team-a", Repo: bare, Namespaces: []string{"tenant-a"}}},
		Live:     clusterstate.LiveView{"tenant-a": {"only-live": {Phase: "Running"}}},
		ListsVMs: func(string) bool { return false },
	})
	if got := inv.Projects[0].Namespaces; len(got) != 1 || len(got[0].VMs) != 0 {
		t.Errorf("namespaces = %+v, want tenant-a with no VMs", got)
	}
}

// Under Any a project's repo is flagged for what the install's tenant repos may
// not apply, not for every kind dotvirt has no row for.
func TestBuildFlagsKindsByTheInstallPolicy(t *testing.T) {
	doc := func(apiVersion, kind, name string) string {
		return "apiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: " + name + "\n  namespace: a-dev\n"
	}
	bare := seedRepoFiles(t, map[string]string{
		"a-dev/web.yaml":  doc("apps/v1", "Deployment", "web"),
		"a-dev/role.yaml": doc("rbac.authorization.k8s.io/v1", "Role", "widen"),
		"a-dev/vlan.yaml": doc("k8s.cni.cncf.io/v1", "NetworkAttachmentDefinition", "vlan"),
		"a-dev/vm.yaml":   vmYAML("vm", "a-dev"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	build := func(rule changeset.KindRule) []string {
		p := Build(Inputs{
			Branch: "main", Repos: repos, Kinds: rule,
			Projects: []project.ProjectInfo{{Name: "team-a", Repo: bare, Namespaces: []string{"a-dev"}}},
		}).Projects[0]
		var out []string
		for _, r := range p.PlatformKinds {
			out = append(out, r.Name)
		}
		slices.Sort(out)
		return out
	}
	if got := build(changeset.KindRule{Policy: tenancy.AnyPolicy()}); !slices.Equal(got, []string{"vlan", "widen"}) {
		t.Errorf("any: %v", got)
	}
	if got := build(changeset.KindRule{}); !slices.Equal(got, []string{"vlan", "web", "widen"}) {
		t.Errorf("listed: %v", got)
	}
}
