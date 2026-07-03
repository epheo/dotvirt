package changeset

import (
	"errors"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// seedBareWithRunning creates a bare repo with alpha/web on main, plus a
// running branch where web has drifted (an extra label) and a cluster-only VM
// alpha/copy exists — the exporter's view after an out-of-band create (e.g. a
// clone target).
func seedBareWithRunning(t *testing.T) string {
	t.Helper()
	vm := func(name, labels string) string {
		return "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: " + name +
			"\n  namespace: alpha\n" + labels + "spec:\n  runStrategy: Always\n"
	}
	bare, work := seedWork(t, map[string][]byte{"alpha/web.yaml": []byte(vm("web", ""))})

	gitRun(t, work, "checkout", "-qb", "running")
	writeWorkFile(t, work, "alpha/web.yaml", []byte(vm("web", "  labels:\n    env: prod\n")))
	writeWorkFile(t, work, "alpha/copy.yaml", []byte(vm("copy", "")))
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-qm", "export live state")
	gitRun(t, work, "push", "-q", "origin", "running")
	return bare
}

// A VM on running but not on main (a clone target) adopts as a CREATE carrying
// the running-branch manifest verbatim, and proposes to the same path.
func TestAdoptStagesCreateForClusterOnlyVM(t *testing.T) {
	bare := seedBareWithRunning(t)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	view, err := c.Adopt(id, proj, "alpha", "copy")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if view.Count != 1 || len(view.Items) != 1 {
		t.Fatalf("want 1 staged item, got count=%d items=%d", view.Count, len(view.Items))
	}
	it := view.Items[0]
	if it.Kind != string(draft.KindCreate) || it.Namespace != "alpha" || it.Name != "copy" {
		t.Fatalf("unexpected item: %+v", it)
	}
	if !strings.Contains(it.YAML, "name: copy") {
		t.Errorf("item YAML should carry the running-branch manifest, got:\n%s", it.YAML)
	}
	if len(it.Changes) != 1 || it.Changes[0].Action != "add" {
		t.Errorf("want one add change, got %+v", it.Changes)
	}

	// The changeset item must land the manifest at its running-branch path.
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	items, err := c.toChangesetItems(entries)
	if err != nil {
		t.Fatalf("toChangesetItems: %v", err)
	}
	if len(items) != 1 || items[0].Path != "alpha/copy.yaml" || items[0].NewContent == nil {
		t.Fatalf("want a create at alpha/copy.yaml, got %+v", items)
	}
	if !strings.Contains(string(items[0].NewContent), "name: copy") {
		t.Errorf("NewContent should be the running-branch manifest")
	}
}

// A VM on both branches keeps the edit-adopt path: the staged entry is a
// KindEdit making base match running.
func TestAdoptStagesEditForDriftedVM(t *testing.T) {
	bare := seedBareWithRunning(t)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	view, err := c.Adopt(id, proj, "alpha", "web")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(view.Items) != 1 || view.Items[0].Kind != string(draft.KindEdit) {
		t.Fatalf("want one edit item, got %+v", view.Items)
	}
	entries, _ := c.store.List(id.Username, proj.Name)
	if len(entries) != 1 || entries[0].Edit == nil || entries[0].Edit.SetLabels["env"] != "prod" {
		t.Fatalf("want an edit staging the drifted label, got %+v", entries)
	}
}

// AdoptNamespace stages only the untracked (cluster-only) VMs in a namespace:
// alpha/copy is on running but not main, while alpha/web is tracked (and drifted) —
// so a single bulk adopt picks up copy and leaves web for the per-VM drift path.
func TestAdoptNamespaceStagesOnlyUntracked(t *testing.T) {
	bare := seedBareWithRunning(t)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	view, err := c.AdoptNamespace(id, proj, "alpha")
	if err != nil {
		t.Fatalf("AdoptNamespace: %v", err)
	}
	if view.Count != 1 || len(view.Items) != 1 {
		t.Fatalf("want 1 staged item (alpha/copy), got count=%d items=%d", view.Count, len(view.Items))
	}
	if it := view.Items[0]; it.Kind != string(draft.KindCreate) || it.Name != "copy" {
		t.Fatalf("want a create for alpha/copy, got %+v", it)
	}

	// Idempotent: copy is staged but not yet on base, so a second call re-stages the
	// same entry rather than adding a duplicate.
	view, err = c.AdoptNamespace(id, proj, "alpha")
	if err != nil {
		t.Fatalf("AdoptNamespace (rerun): %v", err)
	}
	if view.Count != 1 {
		t.Fatalf("re-adopt should be idempotent, got count=%d", view.Count)
	}
}

// A namespace with no cluster-only VMs has nothing to adopt — a clear ErrInvalid so
// the UI can say so rather than opening an empty PR.
func TestAdoptNamespaceNothingUntracked(t *testing.T) {
	bare := seedBareWithRunning(t)
	c := newTestCoordinator(t)

	_, err := c.AdoptNamespace(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare}, "beta")
	if !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("want model.ErrInvalid for a namespace with no untracked VMs, got %v", err)
	}
}

// stageProjectAdoption stamps each of a project's namespaces with the dotvirt.io/repo
// annotation (+ an owners RoleBinding) into the platform draft — the staging core of
// AdoptProject, exercised without a forge.
func TestStageProjectAdoptionStampsRepoOnEveryNamespace(t *testing.T) {
	c := newTestCoordinator(t)
	id := "alice"
	const platform = "platform"
	target := project.ProjectInfo{Name: "team-a", Namespaces: []string{"team-a", "team-a-db"}}
	repoURL := "https://forge.example/acme/team-a.git"

	if err := c.stageProjectAdoption(id, platform, target, repoURL, []string{"alice", "bob"}); err != nil {
		t.Fatalf("stageProjectAdoption: %v", err)
	}
	entries, err := c.store.List(id, platform)
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}

	nsByName := map[string]draft.Entry{}
	rbByName := map[string]draft.Entry{}
	for _, e := range entries {
		switch e.Resource {
		case draft.ResourceNamespace:
			nsByName[e.Name] = e
		case draft.ResourceRoleBinding:
			rbByName[e.Name] = e
		}
	}
	for _, ns := range target.Namespaces {
		e, ok := nsByName[ns]
		if !ok {
			t.Fatalf("no namespace entry staged for %q", ns)
		}
		if e.Kind != draft.KindCreate {
			t.Errorf("namespace %q: want KindCreate, got %q", ns, e.Kind)
		}
		if !strings.Contains(e.Manifest, repoURL) || !strings.Contains(e.Manifest, "dotvirt.io/repo") {
			t.Errorf("namespace %q manifest must carry the dotvirt.io/repo annotation, got:\n%s", ns, e.Manifest)
		}
		if !strings.Contains(e.Manifest, "dotvirt.io/project") || !strings.Contains(e.Manifest, "team-a") {
			t.Errorf("namespace %q manifest must carry the dotvirt.io/project label, got:\n%s", ns, e.Manifest)
		}
		if _, ok := rbByName[ns+"-admins"]; !ok {
			t.Errorf("no owners RoleBinding staged for namespace %q", ns)
		}
	}
}

func TestAdoptAbsentFromRunningNotFound(t *testing.T) {
	bare := seedBareWithRunning(t)
	c := newTestCoordinator(t)

	_, err := c.Adopt(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare}, "alpha", "ghost")
	if !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("want model.ErrNotFound, got %v", err)
	}
}
