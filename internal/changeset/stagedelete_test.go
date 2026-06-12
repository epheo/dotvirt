package changeset

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// seedBare creates a bare repo with one VM manifest (alpha/web) on main and
// returns its path, usable as a RepoSet/project repo URL.
func seedBare(t *testing.T) string {
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
	manifest := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Always\n"
	if err := os.WriteFile(filepath.Join(work, "web.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "-A")
	run(work, "commit", "-qm", "seed")
	run(work, "remote", "add", "origin", bare)
	run(work, "push", "-q", "origin", "main")
	return bare
}

// newTestCoordinator builds a Coordinator over a disk draft store and a
// push-disabled RepoSet (long poll interval so the background poll never fires).
func newTestCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatalf("draft.Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := git.NewRepoSet(ctx, "", "", false, make(chan struct{}, 1), nil, time.Hour)
	return New(store, repos, nil, nil, "main", "dotvirt/proposed", "running")
}

func TestStageDeleteStagesRemoval(t *testing.T) {
	bare := seedBare(t)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	view, err := c.StageDelete(id, proj, "alpha", "web")
	if err != nil {
		t.Fatalf("StageDelete: %v", err)
	}
	if view.Count != 1 || len(view.Items) != 1 {
		t.Fatalf("want 1 staged item, got count=%d items=%d", view.Count, len(view.Items))
	}
	it := view.Items[0]
	if it.Kind != string(draft.KindDelete) || it.Namespace != "alpha" || it.Name != "web" {
		t.Fatalf("unexpected item: %+v", it)
	}
	if len(it.Changes) != 1 || it.Changes[0].Action != "remove" {
		t.Fatalf("want one remove change, got %+v", it.Changes)
	}
}

func TestStageDeleteAbsentNotFound(t *testing.T) {
	bare := seedBare(t)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	_, err := c.StageDelete(id, proj, "alpha", "ghost")
	if !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("want model.ErrNotFound, got %v", err)
	}
}
