package changeset

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// newTestCoordinator builds a Coordinator over a disk draft store and a RepoSet
// with a long poll interval, so the background poll never fires. push enables
// pushes, for a test whose assertion is the bare remote's proposed branch.
func newTestCoordinator(t *testing.T, push bool) *Coordinator {
	t.Helper()
	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatalf("draft.Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := git.NewRepoSet(ctx, "", nil, push, nil, time.Hour)
	return New(store, repos, nil, nil, nil, nil, "main", "dotvirt/proposed")
}

func TestStageDeleteStagesRemoval(t *testing.T) {
	bare := seedBare(t)
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	view, err := c.StageDelete(id, proj, "", "alpha", "web")
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
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	_, err := c.StageDelete(id, proj, "", "alpha", "ghost")
	if !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("want model.ErrNotFound, got %v", err)
	}
}

// A base branch the mirror lacks is a retryable outage the client can act on,
// not an unexplained 500.
func TestStageDeleteUnmirroredBranchIsUnavailable(t *testing.T) {
	bare := seedBareFiles(t, nil)
	gitRun(t, bare, "branch", "-m", "main", "trunk")
	c := newTestCoordinator(t, false)

	_, err := c.StageDelete(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare}, "", "alpha", "web")
	if !errors.Is(err, model.ErrUnavailable) || !errors.Is(err, git.ErrNoBranch) {
		t.Fatalf("want a classified ErrUnavailable that is still git.ErrNoBranch, got %v", err)
	}
}
