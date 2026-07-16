package changeset

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

func TestStageCreateRejectsBadNames(t *testing.T) {
	bare := seedBare(t)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	// Name and namespace become the manifest's repo path; traversal-shaped and
	// non-DNS-1123 values must be rejected at stage time.
	for _, spec := range []map[string]string{
		{"name": "../evil", "namespace": "alpha"},
		{"name": "web", "namespace": "../../platform"},
		{"name": "a/b", "namespace": "alpha"},
		{"name": "", "namespace": "alpha"},
		{"name": "Web", "namespace": "alpha"},
	} {
		raw, _ := json.Marshal(spec)
		if _, err := c.StageCreate(id, proj, raw); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("StageCreate(%v): want ErrInvalid, got %v", spec, err)
		}
	}
}

func TestSiblingRepoURL(t *testing.T) {
	got := siblingRepoURL("https://forge/dotvirt/platform.git", "team-a")
	if want := "https://forge/dotvirt/team-a.git"; got != want {
		t.Errorf("siblingRepoURL = %q, want %q", got, want)
	}
	if got := siblingRepoURL("noslash", "x"); got != "" {
		t.Errorf("siblingRepoURL(no slash) = %q, want empty", got)
	}
}
