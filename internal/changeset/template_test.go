package changeset

import (
	"context"
	"errors"
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

const libraryTemplate = `apiVersion: template.kubevirt.io/v1beta1
kind: VirtualMachineTemplate
metadata:
  name: base
spec:
  parameters:
    - name: NAME
      generate: expression
      from: "base-[a-z0-9]{5}"
  virtualMachine:
    apiVersion: kubevirt.io/v1
    kind: VirtualMachine
    metadata:
      name: ${NAME}
    spec:
      runStrategy: Halted
      template:
        spec:
          domain:
            devices: {}
`

func TestStageDeployFromTemplate(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{"templates/base.yaml": []byte(libraryTemplate)})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	view, err := c.StageDeployFromTemplate(id, proj, proj, model.DeployTemplateRequest{
		Template: "base", Namespace: "alpha", Name: "web-01",
	})
	if err != nil {
		t.Fatalf("StageDeployFromTemplate: %v", err)
	}
	if view.Count != 1 {
		t.Fatalf("want 1 staged item, got %d", view.Count)
	}
	it := view.Items[0]
	if it.Kind != string(draft.KindCreate) || it.Namespace != "alpha" || it.Name != "web-01" {
		t.Fatalf("unexpected item: %+v", it)
	}
	if len(it.Changes) != 1 || it.Changes[0].Field != "Deploy from template p/base" {
		t.Fatalf("unexpected changes: %+v", it.Changes)
	}
	if !strings.Contains(it.YAML, "namespace: alpha") || !strings.Contains(it.YAML, "name: web-01") {
		t.Fatalf("staged manifest wrong:\n%s", it.YAML)
	}
}

func TestStageDeployFromTemplateGeneratesName(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{"templates/base.yaml": []byte(libraryTemplate)})
	c := newTestCoordinator(t)
	view, err := c.StageDeployFromTemplate(auth.Identity{Username: "alice"},
		project.ProjectInfo{Name: "p", Repo: bare}, project.ProjectInfo{Name: "p", Repo: bare},
		model.DeployTemplateRequest{Template: "base", Namespace: "alpha"})
	if err != nil {
		t.Fatalf("StageDeployFromTemplate: %v", err)
	}
	if !strings.HasPrefix(view.Items[0].Name, "base-") {
		t.Fatalf("name %q not generated from the template pattern", view.Items[0].Name)
	}
}

func TestStageDeployFromTemplateSharedLibrary(t *testing.T) {
	// The template lives in the platform repo; the VM stages into the tenant's.
	platform := seedBareFiles(t, map[string][]byte{"templates/base.yaml": []byte(libraryTemplate)})
	tenant := seedBareFiles(t, nil)
	c := newTestCoordinator(t)
	view, err := c.StageDeployFromTemplate(auth.Identity{Username: "alice"},
		project.ProjectInfo{Name: "p", Repo: tenant}, project.ProjectInfo{Name: "platform", Repo: platform},
		model.DeployTemplateRequest{Library: "platform", Template: "base", Namespace: "alpha", Name: "web-01"})
	if err != nil {
		t.Fatalf("StageDeployFromTemplate: %v", err)
	}
	if view.Items[0].Changes[0].Field != "Deploy from template platform/base" {
		t.Fatalf("unexpected changes: %+v", view.Items[0].Changes)
	}
}

func TestStageDeployFromTemplateErrors(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{
		"templates/base.yaml": []byte(libraryTemplate),
		"alpha/web-01.yaml":   []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web-01\n  namespace: alpha\n"),
	})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	for _, tc := range []struct {
		name string
		req  model.DeployTemplateRequest
		want error
	}{
		{"missing template", model.DeployTemplateRequest{Template: "nope", Namespace: "alpha"}, model.ErrNotFound},
		{"traversal template name", model.DeployTemplateRequest{Template: "../secrets", Namespace: "alpha"}, model.ErrInvalid},
		{"target already committed", model.DeployTemplateRequest{Template: "base", Namespace: "alpha", Name: "web-01"}, model.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.StageDeployFromTemplate(id, proj, proj, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

// Templates blueprint Halted; the PowerOn flag must flip only the rendered
// manifest's run state, leaving the default deploy untouched.
func TestStageDeployFromTemplatePowerOn(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{"templates/base.yaml": []byte(libraryTemplate)})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	for _, tc := range []struct {
		name    string
		powerOn bool
		want    string
	}{
		{"default stays halted", false, "runStrategy: Halted"},
		{"powerOn boots", true, "runStrategy: Always"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view, err := c.StageDeployFromTemplate(id, proj, proj, model.DeployTemplateRequest{
				Template: "base", Namespace: "alpha", Name: "web-" + tc.name[:2], PowerOn: tc.powerOn,
			})
			if err != nil {
				t.Fatalf("StageDeployFromTemplate: %v", err)
			}
			if got := view.Items[len(view.Items)-1].YAML; !strings.Contains(got, tc.want) {
				t.Fatalf("want %q in staged manifest:\n%s", tc.want, got)
			}
		})
	}
}

func TestStageUpdateTemplate(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{"templates/base.yaml": []byte(libraryTemplate)})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	edited := strings.Replace(libraryTemplate, "name: base", "name: base\n  annotations:\n    description: edited", 1)
	view, err := c.StageUpdateTemplate(id, proj, model.UpdateTemplateRequest{Name: "base", YAML: edited})
	if err != nil {
		t.Fatalf("StageUpdateTemplate: %v", err)
	}
	if view.Count != 1 {
		t.Fatalf("want 1 staged item, got %d", view.Count)
	}
	it := view.Items[0]
	if it.Kind != string(draft.KindEdit) || it.Resource != string(draft.ResourceTemplate) || it.Name != "base" {
		t.Fatalf("unexpected item: %+v", it)
	}
	if it.Changes[0].Field != "Edit template" || it.Changes[0].To != "base" {
		t.Fatalf("unexpected changes: %+v", it.Changes)
	}
	if !strings.Contains(it.YAML, "description: edited") {
		t.Fatalf("staged YAML is not the edited content:\n%s", it.YAML)
	}
}

func TestStageUpdateTemplateErrors(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{"templates/base.yaml": []byte(libraryTemplate)})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	for _, tc := range []struct {
		name string
		req  model.UpdateTemplateRequest
		want error
	}{
		{"traversal name", model.UpdateTemplateRequest{Name: "../base", YAML: libraryTemplate}, model.ErrInvalid},
		{"garbage YAML", model.UpdateTemplateRequest{Name: "base", YAML: ":\n:not yaml"}, model.ErrInvalid},
		{"wrong kind", model.UpdateTemplateRequest{Name: "base", YAML: "apiVersion: v1\nkind: Pod\nmetadata:\n  name: x\n"}, model.ErrInvalid},
		{"not in library", model.UpdateTemplateRequest{Name: "ghost", YAML: libraryTemplate}, model.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.StageUpdateTemplate(id, proj, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestStageSaveTemplate(t *testing.T) {
	vm := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Always\n"
	bare := seedBareFiles(t, map[string][]byte{"alpha/web.yaml": []byte(vm)})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	view, err := c.StageSaveTemplate(id, proj, proj, model.SaveTemplateRequest{
		Name: "web-golden", Description: "golden web", SourceNamespace: "alpha", SourceName: "web",
	})
	if err != nil {
		t.Fatalf("StageSaveTemplate: %v", err)
	}
	if view.Count != 1 {
		t.Fatalf("want 1 staged item, got %d", view.Count)
	}
	it := view.Items[0]
	if it.Resource != string(draft.ResourceTemplate) || it.Name != "web-golden" {
		t.Fatalf("unexpected item: %+v", it)
	}
	if it.Changes[0].Field != "Save as template" || it.Changes[0].To != "web-golden" {
		t.Fatalf("unexpected changes: %+v", it.Changes)
	}
	if !strings.Contains(it.YAML, "kind: VirtualMachineTemplate") || !strings.Contains(it.YAML, "name: ${NAME}") {
		t.Fatalf("staged template wrong:\n%s", it.YAML)
	}
}

func TestStageSaveTemplateErrors(t *testing.T) {
	vm := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\n"
	bare := seedBareFiles(t, map[string][]byte{
		"alpha/web.yaml":          []byte(vm),
		"templates/existing.yaml": []byte(libraryTemplate),
	})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	for _, tc := range []struct {
		name string
		req  model.SaveTemplateRequest
		want error
	}{
		{"VM not in git", model.SaveTemplateRequest{Name: "t", SourceNamespace: "alpha", SourceName: "ghost"}, model.ErrNotFound},
		{"bad template name", model.SaveTemplateRequest{Name: "Bad/Name", SourceNamespace: "alpha", SourceName: "web"}, model.ErrInvalid},
		{"duplicate template", model.SaveTemplateRequest{Name: "existing", SourceNamespace: "alpha", SourceName: "web"}, model.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.StageSaveTemplate(id, proj, proj, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

// Proposing a draft holding a deploy + a saved template must commit both files
// verbatim — templates ride the same changeset path as every other manifest.
func TestProposeCommitsTemplateEntries(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{
		"templates/base.yaml": []byte(libraryTemplate),
		"alpha/web.yaml":      []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Always\n"),
	})
	// A pushing RepoSet: the assertion is the bare repo's proposed branch.
	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatalf("draft.Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := New(store, git.NewRepoSet(ctx, "", nil, true, nil, time.Hour), nil, nil, "main", "dotvirt/proposed", "running")
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	if _, err := c.StageDeployFromTemplate(id, proj, proj, model.DeployTemplateRequest{
		Template: "base", Namespace: "alpha", Name: "web-02",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StageSaveTemplate(id, proj, proj, model.SaveTemplateRequest{
		Name: "web-golden", SourceNamespace: "alpha", SourceName: "web",
	}); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(libraryTemplate, "name: base", "name: base\n  annotations:\n    description: edited", 1)
	if _, err := c.StageUpdateTemplate(id, proj, model.UpdateTemplateRequest{Name: "base", YAML: edited}); err != nil {
		t.Fatal(err)
	}
	res, err := c.Propose(id, proj, model.ProposeRequest{Title: "t"})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	for _, path := range []string{"alpha/web-02.yaml", "templates/web-golden.yaml"} {
		cmd := exec.Command("git", "-C", bare, "cat-file", "-p", res.Branch+":"+path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s not committed on %s: %v\n%s", path, res.Branch, err, out)
		}
	}
	out, err := exec.Command("git", "-C", bare, "cat-file", "-p", res.Branch+":templates/base.yaml").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "description: edited") {
		t.Errorf("templates/base.yaml not replaced on %s: %v\n%s", res.Branch, err, out)
	}
}
