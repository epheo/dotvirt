package changeset

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

func requireNothingStaged(t *testing.T, c *Coordinator, id auth.Identity, proj project.ProjectInfo) {
	t.Helper()
	if entries, err := c.store.List(id.Username, proj.Name); err != nil || len(entries) != 0 {
		t.Fatalf("a preview must stage nothing, got %+v (%v)", entries, err)
	}
}

// A preview is the stage minus the store write: the same path and bytes.
func TestPreviewCreateVMIsWhatStageRecords(t *testing.T) {
	bare := seedBare(t)
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}
	raw := json.RawMessage(`{"name":"db","namespace":"alpha","instancetype":"u1.medium","preference":"fedora",
		"osImage":{"name":"fedora","namespace":"kv"},"diskSize":"40Gi","running":true}`)

	p, err := c.PreviewCreateVM(proj, raw)
	if err != nil {
		t.Fatalf("PreviewCreateVM: %v", err)
	}
	requireNothingStaged(t, c, id, proj)
	if p.Project != "p" || len(p.Files) != 1 {
		t.Fatalf("want one file in project p, got %+v", p)
	}
	f := p.Files[0]
	if f.Kind != "create" || f.Path != "alpha/db.yaml" || !strings.Contains(f.YAML, "name: u1.medium") {
		t.Fatalf("unexpected preview file: %+v", f)
	}

	if _, err := c.StageCreateVM(id, proj, raw); err != nil {
		t.Fatalf("StageCreateVM: %v", err)
	}
	entries, _ := c.store.List(id.Username, proj.Name)
	if len(entries) != 1 || entries[0].SourceFile != f.Path || entries[0].Manifest != f.YAML {
		t.Fatalf("the staged entry must be the previewed file, got %+v", entries)
	}
}

// The typed password never comes back, even in a preview: only its hash.
func TestPreviewCreateVMHashesPassword(t *testing.T) {
	c := newTestCoordinator(t, false)
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t)}
	const typed = "hunter2-plaintext"
	p, err := c.PreviewCreateVM(proj, json.RawMessage(`{"name":"db","namespace":"alpha","instancetype":"u1.medium",
		"preference":"fedora","osImage":{"name":"fedora","namespace":"kv"},"cloudInit":{"password":"`+typed+`"}}`))
	if err != nil {
		t.Fatalf("PreviewCreateVM: %v", err)
	}
	if y := p.Files[0].YAML; strings.Contains(y, typed) || !strings.Contains(y, "password: $2a$") {
		t.Fatalf("the preview must carry the hash, never the typed password:\n%s", y)
	}
}

// A preview refuses what staging refuses, with the same error kinds.
func TestPreviewCreateVMRefusesLikeStage(t *testing.T) {
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t)}
	for _, tc := range []struct {
		spec string
		want error
	}{
		{`{"name":"../evil","namespace":"alpha","instancetype":"u1.medium","preference":"fedora","osImage":{"name":"f"}}`, model.ErrInvalid},
		{`{"name":"db","namespace":"alpha","preference":"fedora","osImage":{"name":"f"}}`, model.ErrInvalid},
		{`{"name":"web","namespace":"alpha","instancetype":"u1.medium","preference":"fedora","osImage":{"name":"f"}}`, model.ErrConflict},
	} {
		if _, err := c.PreviewCreateVM(proj, json.RawMessage(tc.spec)); !errors.Is(err, tc.want) {
			t.Errorf("PreviewCreateVM(%s): want %v, got %v", tc.spec, tc.want, err)
		}
	}
	if _, err := c.PreviewCreateVM(project.ProjectInfo{Name: "norepo"}, json.RawMessage(`{}`)); !errors.Is(err, model.ErrConflict) {
		t.Errorf("a repoless project must refuse as ErrConflict, got %v", err)
	}
	requireNothingStaged(t, c, id, proj)
}

// A network-family create of an object git already declares previews as the
// edit staging would record: the declaring file, replaced.
func TestPreviewCreateResolvesDeclaredAsEdit(t *testing.T) {
	udn := "apiVersion: k8s.ovn.org/v1\nkind: UserDefinedNetwork\nmetadata:\n  name: web-net\n  namespace: alpha\nspec:\n  topology: Layer2\n"
	bare := seedBareFiles(t, map[string][]byte{"nets/web-net.yaml": []byte(udn)})
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	declared, err := c.PreviewCreate(proj, draft.ResourceNetwork, json.RawMessage(`{"name":"web-net","namespace":"alpha","scope":"project","subnets":["10.40.0.0/16"]}`))
	if err != nil {
		t.Fatalf("PreviewCreate: %v", err)
	}
	if f := declared.Files[0]; f.Kind != "edit" || f.Path != "nets/web-net.yaml" || !strings.Contains(f.YAML, "10.40.0.0/16") {
		t.Fatalf("want an edit of the declaring file, got %+v", f)
	}

	fresh, err := c.PreviewCreate(proj, draft.ResourceNetwork, json.RawMessage(`{"name":"web-backend","namespace":"alpha","scope":"project"}`))
	if err != nil {
		t.Fatalf("PreviewCreate: %v", err)
	}
	if f := fresh.Files[0]; f.Kind != "create" || f.Path != "alpha/networks/web-backend.yaml" {
		t.Fatalf("want a new file at the rendered path, got %+v", f)
	}

	if _, err := c.PreviewCreate(proj, draft.ResourceNetwork, json.RawMessage(`{"name":"x","namespace":"alpha","subnets":["nope"]}`)); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("a bad subnet must refuse as ErrInvalid, got %v", err)
	}
	if _, err := c.PreviewCreate(proj, draft.ResourceVM, json.RawMessage(`{}`)); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("a resource with no create form must refuse as ErrInvalid, got %v", err)
	}
	requireNothingStaged(t, c, id, proj)
}

// A project preview renders the namespace and owners grant against the repo
// the create would make, and never calls the forge: the fixture fails any call.
func TestPreviewCreateProjectCallsNoForge(t *testing.T) {
	f := newProposeFixture(t)
	p, err := f.c.PreviewCreateProject(f.proj, json.RawMessage(`{"name":"team-a","owners":["bob"],
		"vmNetwork":{"name":"team-a-net","subnet":"10.60.0.0/16"}}`), nil)
	if err != nil {
		t.Fatalf("PreviewCreateProject: %v", err)
	}
	requireNothingStaged(t, f.c, f.id, f.proj)
	if p.Project != "p" || len(p.Files) != 2 {
		t.Fatalf("want the namespace and its owners grant, got %+v", p)
	}
	ns, rb := p.Files[0], p.Files[1]
	if !strings.Contains(ns.YAML, "team-a.git") || !strings.Contains(ns.YAML, "10.60.0.0/16") {
		t.Fatalf("namespace must name its repo and carry its VM Network:\n%s", ns.YAML)
	}
	if !strings.Contains(rb.YAML, "bob") {
		t.Fatalf("owners grant must name bob:\n%s", rb.YAML)
	}

	if _, err := f.c.PreviewCreateProject(f.proj, json.RawMessage(`{"name":"platform"}`), nil); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("the reserved name must refuse as ErrInvalid, got %v", err)
	}
}

// Without a forge no repo can be made, so the preview refuses like the stage.
func TestPreviewCreateProjectNeedsForge(t *testing.T) {
	c := newTestCoordinator(t, false)
	_, err := c.PreviewCreateProject(project.ProjectInfo{Name: "platform", Repo: "https://forge.example/dotvirt/platform.git"},
		json.RawMessage(`{"name":"team-a"}`), nil)
	if !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "forge not configured") {
		t.Fatalf("want the stage's forge refusal, got %v", err)
	}
}

// A namespace preview renders the platform file labeled to the joining project.
func TestPreviewCreateNamespace(t *testing.T) {
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	plat := project.ProjectInfo{Name: "platform", Repo: seedBare(t)}
	join := project.ProjectInfo{Name: "team-a", Repo: "https://forge.example/dotvirt/team-a.git"}
	p, err := c.PreviewCreateNamespace(plat, join, json.RawMessage(`{"name":"team-a-dev"}`), nil)
	if err != nil {
		t.Fatalf("PreviewCreateNamespace: %v", err)
	}
	requireNothingStaged(t, c, id, plat)
	if p.Project != "platform" || len(p.Files) != 1 || !strings.Contains(p.Files[0].YAML, "team-a") {
		t.Fatalf("want one platform file labeled to team-a, got %+v", p)
	}
}

// Template previews go through the same render as deploys.
func TestPreviewDeployFromTemplate(t *testing.T) {
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: seedBareFiles(t, map[string][]byte{"templates/base.yaml": []byte(libraryTemplate)})}
	p, err := c.PreviewDeployFromTemplate(proj, proj, model.DeployTemplateRequest{Template: "base", Namespace: "alpha", Name: "web-9"})
	if err != nil {
		t.Fatalf("PreviewDeployFromTemplate: %v", err)
	}
	requireNothingStaged(t, c, id, proj)
	if f := p.Files[0]; f.Kind != "create" || f.Path != "alpha/web-9.yaml" || !strings.Contains(f.YAML, "name: web-9") {
		t.Fatalf("unexpected template preview: %+v", f)
	}
	if _, err := c.PreviewDeployFromTemplate(proj, proj, model.DeployTemplateRequest{Template: "absent", Namespace: "alpha"}); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("an absent template must refuse as ErrNotFound, got %v", err)
	}
}
