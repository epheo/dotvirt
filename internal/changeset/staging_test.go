package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
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

// A spec the renderer refuses fails at the form as ErrInvalid, not at propose,
// and leaves nothing staged.
func TestStageCreateRejectsIncompleteSpec(t *testing.T) {
	bare := seedBare(t)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	full := map[string]any{
		"name": "db", "namespace": "alpha", "instancetype": "u1.medium", "preference": "fedora",
		"osImage": map[string]string{"name": "fedora", "namespace": "kv"},
	}
	for _, missing := range []string{"instancetype", "preference", "osImage"} {
		spec := maps.Clone(full)
		delete(spec, missing)
		raw, _ := json.Marshal(spec)
		if _, err := c.StageCreate(id, proj, raw); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("StageCreate without %s: want ErrInvalid, got %v", missing, err)
		}
	}
	if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 0 {
		t.Errorf("a refused spec must not be staged, got %+v", entries)
	}
}

// A wizard VM is rendered once, at stage time: the preview, the persisted draft
// and the proposed commit are the same bytes, and the typed cloud-init password
// exists nowhere past the request - only its hash does.
func TestStageCreateCommitsPreviewedManifest(t *testing.T) {
	bare := seedBare(t)
	// A pushing RepoSet: the assertion is the bare repo's proposed branch.
	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatalf("draft.Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := New(store, git.NewRepoSet(ctx, "", nil, true, nil, time.Hour), nil, nil, nil, nil, "main", "dotvirt/proposed")
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	const typed = "hunter2-plaintext"
	raw := json.RawMessage(`{"name":"db","namespace":"alpha","instancetype":"u1.medium","preference":"fedora",
		"osImage":{"name":"fedora","namespace":"kv"},"diskSize":"40Gi","running":true,
		"cloudInit":{"user":"admin","password":"` + typed + `"}}`)
	view, err := c.StageCreate(id, proj, raw)
	if err != nil {
		t.Fatalf("StageCreate: %v", err)
	}
	if len(view.Items) != 1 || view.Items[0].Kind != "create" || view.Items[0].YAML == "" {
		t.Fatalf("want one create item carrying its YAML, got %+v", view.Items)
	}
	it := view.Items[0]
	if strings.Contains(it.YAML, typed) || !strings.Contains(it.YAML, "password: $2a$") {
		t.Fatalf("the preview must carry the hash, never the typed password:\n%s", it.YAML)
	}
	entries, err := store.List(id.Username, proj.Name)
	if err != nil || len(entries) != 1 {
		t.Fatalf("store.List: %v, %+v", err, entries)
	}
	if entries[0].Manifest != it.YAML || entries[0].SourceFile != "alpha/db.yaml" {
		t.Fatalf("the persisted entry must be the previewed manifest at its path, got %+v", entries[0])
	}
	rows := map[string]string{}
	for _, ch := range it.Changes {
		rows[ch.Field] = ch.To
	}
	if rows["Create VM"] != "alpha/db" || rows["Instance type"] != "u1.medium" || rows["Power"] != "On" {
		t.Errorf("rows must summarize the rendered manifest, got %+v", it.Changes)
	}

	res, err := c.Propose(id, proj, model.ProposeRequest{Title: "create db"})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	committed, err := exec.Command("git", "-C", bare, "cat-file", "-p", res.Branch+":alpha/db.yaml").Output()
	if err != nil {
		t.Fatalf("alpha/db.yaml not committed on %s: %v", res.Branch, err)
	}
	if string(committed) != it.YAML {
		t.Fatalf("committed bytes differ from the preview:\n--- committed\n%s\n--- preview\n%s", committed, it.YAML)
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
