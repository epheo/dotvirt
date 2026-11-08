package changeset

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
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
	proj := project.ProjectInfo{Name: "p", Repo: bare, Namespaces: []string{"alpha"}}
	raw := json.RawMessage(`{"name":"db","namespace":"alpha","instancetype":"u1.medium","preference":"fedora",
		"osImage":{"name":"fedora","namespace":"kv"},"diskSize":"40Gi","running":true}`)

	p, err := c.PreviewCreateVM(id, proj, raw)
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
	id := auth.Identity{Username: "alice"}
	c := newTestCoordinator(t, false)
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t), Namespaces: []string{"alpha"}}
	const typed = "hunter2-plaintext"
	p, err := c.PreviewCreateVM(id, proj, json.RawMessage(`{"name":"db","namespace":"alpha","instancetype":"u1.medium",
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
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t), Namespaces: []string{"alpha"}}
	for _, tc := range []struct {
		spec string
		want error
	}{
		{`{"name":"../evil","namespace":"alpha","instancetype":"u1.medium","preference":"fedora","osImage":{"name":"f"}}`, model.ErrInvalid},
		{`{"name":"db","namespace":"alpha","preference":"fedora","osImage":{"name":"f"}}`, model.ErrInvalid},
		{`{"name":"web","namespace":"alpha","instancetype":"u1.medium","preference":"fedora","osImage":{"name":"f"}}`, model.ErrConflict},
	} {
		if _, err := c.PreviewCreateVM(id, proj, json.RawMessage(tc.spec)); !errors.Is(err, tc.want) {
			t.Errorf("PreviewCreateVM(%s): want %v, got %v", tc.spec, tc.want, err)
		}
	}
	if _, err := c.PreviewCreateVM(id, project.ProjectInfo{Name: "norepo"}, json.RawMessage(`{}`)); !errors.Is(err, model.ErrConflict) {
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
	proj := project.ProjectInfo{Name: "p", Repo: bare, Namespaces: []string{"alpha"}}

	declared, err := c.PreviewCreate(id, proj, draft.ResourceNetwork, json.RawMessage(`{"name":"web-net","namespace":"alpha","scope":"project","subnets":["10.40.0.0/16"]}`))
	if err != nil {
		t.Fatalf("PreviewCreate: %v", err)
	}
	if f := declared.Files[0]; f.Kind != "edit" || f.Path != "nets/web-net.yaml" || !strings.Contains(f.YAML, "10.40.0.0/16") {
		t.Fatalf("want an edit of the declaring file, got %+v", f)
	}

	fresh, err := c.PreviewCreate(id, proj, draft.ResourceNetwork, json.RawMessage(`{"name":"web-backend","namespace":"alpha","scope":"project"}`))
	if err != nil {
		t.Fatalf("PreviewCreate: %v", err)
	}
	if f := fresh.Files[0]; f.Kind != "create" || f.Path != "alpha/networks/web-backend.yaml" {
		t.Fatalf("want a new file at the rendered path, got %+v", f)
	}

	if _, err := c.PreviewCreate(id, proj, draft.ResourceNetwork, json.RawMessage(`{"name":"x","namespace":"alpha","subnets":["nope"]}`)); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("a bad subnet must refuse as ErrInvalid, got %v", err)
	}
	if _, err := c.PreviewCreate(id, proj, draft.ResourceVM, json.RawMessage(`{}`)); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("a resource with no create form must refuse as ErrInvalid, got %v", err)
	}
	requireNothingStaged(t, c, id, proj)
}

// A project preview is the files its stage records, grants included, and
// never calls the forge: the repo is made only when the project stages.
func TestPreviewCreateProjectIsWhatStageRecords(t *testing.T) {
	forgeCalls := 0
	f := newProposeFixture(t, func(m, path string) (int, string, bool) {
		forgeCalls++
		if m == "GET" && strings.HasSuffix(path, "/team-a") {
			return http.StatusNotFound, "", true
		}
		if m == "POST" && strings.HasSuffix(path, "/repos") {
			return http.StatusCreated, "{}", true
		}
		return 0, "", false
	})
	raw := json.RawMessage(`{"name":"team-a","owners":["bob"],"vmNetwork":{"name":"team-a-net","subnet":"10.60.0.0/16"}}`)
	p, err := f.c.PreviewCreateProject(f.id, f.platform(), raw, nil)
	if err != nil {
		t.Fatalf("PreviewCreateProject: %v", err)
	}
	if forgeCalls != 0 {
		t.Fatalf("a preview called the forge %d times", forgeCalls)
	}
	requireNothingStaged(t, f.c, f.id, f.platform())
	if p.Project != project.PlatformName || len(p.Files) < 2 {
		t.Fatalf("want the namespace and its grants, got %+v", p)
	}
	if ns := p.Files[0]; !strings.Contains(ns.YAML, "team-a.git") || !strings.Contains(ns.YAML, "10.60.0.0/16") {
		t.Fatalf("namespace must name its repo and carry its VM Network:\n%s", ns.YAML)
	}
	owners := false
	for _, f := range p.Files[1:] {
		owners = owners || (strings.Contains(f.YAML, "team-a-admins") && strings.Contains(f.YAML, "bob"))
	}
	if !owners {
		t.Fatalf("the owners grant must name bob: %+v", p.Files)
	}

	if _, err := f.c.StageCreateProject(f.id, f.platform(), raw, nil); err != nil {
		t.Fatalf("StageCreateProject: %v", err)
	}
	entries, err := f.c.store.List(f.id.Username, f.platform().Name)
	if err != nil || len(entries) != len(p.Files) {
		t.Fatalf("staged %d entries (%v), previewed %d files", len(entries), err, len(p.Files))
	}
	staged := map[string]string{}
	for _, e := range entries {
		staged[e.SourceFile] += e.Manifest
	}
	previewed := map[string]string{}
	for _, f := range p.Files {
		previewed[f.Path] += f.YAML
	}
	if !maps.Equal(staged, previewed) {
		t.Fatalf("the stage recorded other files than the preview showed:\nstaged %v\npreviewed %v", staged, previewed)
	}

	if _, err := f.c.PreviewCreateProject(f.id, f.platform(), json.RawMessage(`{"name":"platform"}`), nil); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("the reserved name must refuse as ErrInvalid, got %v", err)
	}
}

// Without a forge no repo can be made, so the preview refuses like the stage.
func TestPreviewCreateProjectNeedsForge(t *testing.T) {
	c := newTestCoordinator(t, false)
	_, err := c.PreviewCreateProject(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "platform", Repo: seedBare(t)},
		json.RawMessage(`{"name":"team-a"}`), nil)
	if !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "forge not configured") {
		t.Fatalf("want the stage's forge refusal, got %v", err)
	}
}

// A namespace preview renders the platform file labeled to the joining project.
func TestPreviewCreateNamespace(t *testing.T) {
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "alice"}
	plat := project.ProjectInfo{Name: project.PlatformName, Repo: seedBare(t)}
	join := project.ProjectInfo{Name: "team-a", Repo: "https://forge.example/dotvirt/team-a.git"}
	p, err := c.PreviewCreateNamespace(id, plat, join, json.RawMessage(`{"name":"team-a-dev"}`), nil)
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
	proj := project.ProjectInfo{Name: "p", Repo: seedBareFiles(t, map[string][]byte{"templates/base.yaml": []byte(libraryTemplate)}), Namespaces: []string{"alpha"}}
	p, err := c.PreviewDeployFromTemplate(id, proj, proj, model.DeployTemplateRequest{Template: "base", Namespace: "alpha", Name: "web-9"})
	if err != nil {
		t.Fatalf("PreviewDeployFromTemplate: %v", err)
	}
	requireNothingStaged(t, c, id, proj)
	if f := p.Files[0]; f.Kind != "create" || f.Path != "alpha/web-9.yaml" || !strings.Contains(f.YAML, "name: web-9") {
		t.Fatalf("unexpected template preview: %+v", f)
	}
	if _, err := c.PreviewDeployFromTemplate(id, proj, proj, model.DeployTemplateRequest{Template: "absent", Namespace: "alpha"}); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("an absent template must refuse as ErrNotFound, got %v", err)
	}
}

// A file that does not parse costs a preview nothing elsewhere: a new VM
// beside it still renders, as staging would.
func TestPreviewSurvivesUnparsedFile(t *testing.T) {
	id := auth.Identity{Username: "alice"}
	bare := seedBareFiles(t, map[string][]byte{"alpha/bad.yaml": []byte("kind: VirtualMachine\n---\nfoo: [\n")})
	c := newTestCoordinator(t, false)
	proj := project.ProjectInfo{Name: "p", Repo: bare, Namespaces: []string{"alpha"}}
	p, err := c.PreviewCreateVM(id, proj, json.RawMessage(`{"name":"db","namespace":"alpha","instancetype":"u1.medium",
		"preference":"fedora","osImage":{"name":"fedora","namespace":"kv"}}`))
	if err != nil || len(p.Files) != 1 || p.Files[0].Path != "alpha/db.yaml" {
		t.Fatalf("PreviewCreateVM beside an unparsed file = %+v, %v", p, err)
	}
}

// A preview runs staging's gate under the caller's identity: what the caller
// may not create, the preview refuses with the stage's own 403, and a refused
// project preview still makes no repo.
func TestPreviewIsGatedLikeStage(t *testing.T) {
	c := newTestCoordinator(t, false)
	r := &rbac{deny: verbOn("create", "networkpolicies")}
	c.UseAuthorizer(r)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: seedBare(t), Namespaces: []string{"alpha"}}
	raw := json.RawMessage(`{"name":"web","namespace":"alpha"}`)

	_, staged := c.StageCreate(id, proj, draft.ResourceNetworkPolicy, raw)
	_, previewed := c.PreviewCreate(id, proj, draft.ResourceNetworkPolicy, raw)
	if !errors.Is(previewed, model.ErrForbidden) || staged == nil || previewed.Error() != staged.Error() {
		t.Fatalf("preview %v, stage %v: want the same 403", previewed, staged)
	}
	vm := json.RawMessage(`{"name":"db","namespace":"alpha","instancetype":"u1.medium","preference":"fedora","osImage":{"name":"f"}}`)
	r.refuse(verbOn("create", "virtualmachines"))
	if _, err := c.PreviewCreateVM(id, proj, vm); !errors.Is(err, model.ErrForbidden) {
		t.Errorf("a VM the caller may not create: preview err = %v", err)
	}
	r.refuse(nil)
	if _, err := c.PreviewCreateVM(id, proj, vm); err != nil {
		t.Errorf("an allowed preview: %v", err)
	}
	requireNothingStaged(t, c, id, proj)

	c.UseAuthorizer(nil)
	if _, err := c.PreviewCreateVM(id, proj, vm); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("no authorizer: preview err = %v, want the stage's ErrUnavailable", err)
	}

	f := newProposeFixture(t)
	f.c.UseAuthorizer(&rbac{deny: verbOn("create", "namespaces")})
	if _, err := f.c.PreviewCreateProject(f.id, f.platform(), json.RawMessage(`{"name":"team-a"}`), nil); !errors.Is(err, model.ErrForbidden) {
		t.Errorf("a project the caller may not create: preview err = %v", err)
	}
}

// A joining namespace previews with the grants its stage writes: its admins
// and egress grants to the joining project's people and sync account, the
// same files the stage then records.
func TestPreviewCreateNamespaceShowsItsGrants(t *testing.T) {
	bare, _ := seedWork(t, map[string][]byte{
		"rbac/team-a.yaml": grantYAML("team-a", "admin", user("alice"), syncSA("team-a")),
	})
	c := newTestCoordinator(t, false).WithSyncNamespace("openshift-gitops")
	id := auth.Identity{Username: "admin"}
	plat := project.ProjectInfo{Name: project.PlatformName, Repo: bare}
	join := project.ProjectInfo{Name: "team-a", Repo: "acme/team-a.git", Namespaces: []string{"team-a"}}
	raw := json.RawMessage(`{"name":"team-a-db"}`)

	p, err := c.PreviewCreateNamespace(id, plat, join, raw, nil)
	if err != nil {
		t.Fatalf("PreviewCreateNamespace: %v", err)
	}
	requireNothingStaged(t, c, id, plat)
	previewed := map[string]string{}
	for _, f := range p.Files {
		previewed[f.Path] += f.YAML
	}
	grants := 0
	for _, y := range previewed {
		if strings.Contains(y, "team-a-db-admins") || strings.Contains(y, "team-a-db-egress") {
			grants++
		}
	}
	if grants != 2 {
		t.Fatalf("want the admins and egress grants beside the namespace, got %+v", p.Files)
	}

	if _, err := c.StageCreateNamespace(id, plat, join, raw, nil); err != nil {
		t.Fatalf("StageCreateNamespace: %v", err)
	}
	entries, _ := c.store.List(id.Username, plat.Name)
	staged := map[string]string{}
	for _, e := range entries {
		staged[e.SourceFile] += e.Manifest
	}
	if !maps.Equal(staged, previewed) {
		t.Fatalf("the stage recorded other files than the preview showed:\nstaged %v\npreviewed %v", staged, previewed)
	}

	// The grants pass the gate too: one the caller may not make refuses the preview.
	c.UseAuthorizer(&rbac{deny: verbOn("create", "rolebindings")})
	if _, err := c.PreviewCreateNamespace(id, plat, join, json.RawMessage(`{"name":"team-a-web"}`), nil); !errors.Is(err, model.ErrForbidden) {
		t.Errorf("a grant the caller may not make: preview err = %v", err)
	}
}
