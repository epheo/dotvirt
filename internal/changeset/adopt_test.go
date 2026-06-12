package changeset

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	write := func(path, content string) {
		full := filepath.Join(work, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vm := func(name, labels string) string {
		return "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: " + name +
			"\n  namespace: alpha\n" + labels + "spec:\n  runStrategy: Always\n"
	}

	run(dir, "init", "-q", "--bare", "-b", "main", bare)
	run(dir, "init", "-q", "-b", "main", work)
	write("alpha/web.yaml", vm("web", ""))
	run(work, "add", "-A")
	run(work, "commit", "-qm", "seed")
	run(work, "remote", "add", "origin", bare)
	run(work, "push", "-q", "origin", "main")

	run(work, "checkout", "-qb", "running")
	write("alpha/web.yaml", vm("web", "  labels:\n    env: prod\n"))
	write("alpha/copy.yaml", vm("copy", ""))
	run(work, "add", "-A")
	run(work, "commit", "-qm", "export live state")
	run(work, "push", "-q", "origin", "running")
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

func TestAdoptAbsentFromRunningNotFound(t *testing.T) {
	bare := seedBareWithRunning(t)
	c := newTestCoordinator(t)

	_, err := c.Adopt(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare}, "alpha", "ghost")
	if !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("want model.ErrNotFound, got %v", err)
	}
}
