package changeset

import (
	"fmt"
	"log"
	"maps"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/vmtemplate"
)

// StageDeployFromTemplate renders a library template and stages the resulting
// VM manifest into (id, targetProj)'s draft — "Deploy from Template". The
// library may be the target project itself or another readable one (the shared
// platform library); the render is pure computation, so deploying needs only
// the authority to stage into the target project.
func (c *Coordinator) StageDeployFromTemplate(id auth.Identity, targetProj, libraryProj project.ProjectInfo, req model.DeployTemplateRequest) (model.DraftView, error) {
	if err := requireRepo(targetProj); err != nil {
		return model.DraftView{}, err
	}
	if req.Template == "" || req.Namespace == "" {
		return model.DraftView{}, fmt.Errorf("%w: template and namespace are required", model.ErrInvalid)
	}
	// The template name becomes a repo path segment — same trust boundary as
	// project/namespace names.
	if !validName(req.Template) {
		return model.DraftView{}, fmt.Errorf("%w: template name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", model.ErrInvalid, req.Template)
	}
	libRead, err := c.read(libraryProj)
	if err != nil {
		return model.DraftView{}, err
	}
	raw, err := libRead.FileOnBranch(c.baseBranch, git.TemplatesDir+"/"+req.Template+".yaml")
	if err != nil {
		return model.DraftView{}, fmt.Errorf("%w: template %q not in library %q", model.ErrNotFound, req.Template, libraryProj.Name)
	}

	params := maps.Clone(req.Parameters)
	if req.Name != "" {
		if params == nil {
			params = map[string]string{}
		}
		params["NAME"] = req.Name
	}
	rendered, err := c.renderer.Render(raw, params, req.Namespace)
	if err != nil {
		return model.DraftView{}, err
	}
	if !validName(rendered.Name) {
		return model.DraftView{}, fmt.Errorf("%w: rendered VM name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", model.ErrInvalid, rendered.Name)
	}

	targetRead, err := c.read(targetProj)
	if err != nil {
		return model.DraftView{}, err
	}
	path := req.Namespace + "/" + rendered.Name + ".yaml"
	// A deploy must never silently overwrite a committed VM (a duplicate deploy
	// or a generated-name collision) — merging would replace it.
	if _, err := targetRead.FileOnBranch(c.baseBranch, path); err == nil {
		return model.DraftView{}, fmt.Errorf("%w: %s/%s already exists in git", model.ErrConflict, req.Namespace, rendered.Name)
	}

	if err := c.store.Stage(id.Username, targetProj.Name, draft.Entry{
		Kind:         draft.KindCreate,
		Namespace:    req.Namespace,
		Name:         rendered.Name,
		SourceFile:   path,
		Manifest:     string(rendered.Manifest),
		FromTemplate: libraryProj.Name + "/" + req.Template,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, targetProj)
}

// StageSaveTemplate derives a template from an existing VM's git manifest and
// stages it into (id, commitProj)'s draft as templates/<name>.yaml — "Clone to
// Template". commitProj is the library the template lands in (the VM's own
// project, or the platform repo for the shared library); sourceProj owns the
// VM being templated.
func (c *Coordinator) StageSaveTemplate(id auth.Identity, commitProj, sourceProj project.ProjectInfo, req model.SaveTemplateRequest) (model.DraftView, error) {
	if err := requireRepo(commitProj); err != nil {
		return model.DraftView{}, err
	}
	if !validName(req.Name) {
		return model.DraftView{}, fmt.Errorf("%w: template name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", model.ErrInvalid, req.Name)
	}
	srcRead, err := c.read(sourceProj)
	if err != nil {
		return model.DraftView{}, err
	}
	vm, found, err := srcRead.FindVMOnBranch(c.baseBranch, req.SourceNamespace, req.SourceName)
	if err != nil {
		return model.DraftView{}, err
	}
	if !found {
		return model.DraftView{}, fmt.Errorf("%w: VM %s/%s is not in git", model.ErrNotFound, req.SourceNamespace, req.SourceName)
	}
	vmYAML, err := srcRead.FileOnBranch(c.baseBranch, vm.SourceFile)
	if err != nil {
		return model.DraftView{}, err
	}
	tplYAML, err := vmtemplate.Derive(vmYAML, req.Name, req.Description)
	if err != nil {
		return model.DraftView{}, err
	}

	path := git.TemplatesDir + "/" + req.Name + ".yaml"
	commitRead, err := c.read(commitProj)
	if err != nil {
		return model.DraftView{}, err
	}
	if _, err := commitRead.FileOnBranch(c.baseBranch, path); err == nil {
		return model.DraftView{}, fmt.Errorf("%w: template %q already exists in library %q", model.ErrConflict, req.Name, commitProj.Name)
	}

	if err := c.store.Stage(id.Username, commitProj.Name, draft.Entry{
		Kind:       draft.KindCreate,
		Resource:   draft.ResourceTemplate,
		Namespace:  ClusterScopeNS,
		Name:       req.Name,
		SourceFile: path,
		Manifest:   string(tplYAML),
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, commitProj)
}

// seedTemplates pushes the starter library onto a repo dotvirt just created.
// A git-plane write by dotvirt's own credentials: templates sit outside the
// ArgoCD-applied path, so the seed changes nothing on the cluster, and a
// failure only leaves the new library empty — never fails project creation.
func (c *Coordinator) seedTemplates(repoURL string) {
	_, write, err := c.repos.Get(repoURL)
	if err != nil {
		log.Printf("seed templates %s: %v", repoURL, err)
		return
	}
	if _, err := write.Commit(c.baseBranch, "dotvirt: seed starter templates", vmtemplate.SeedFiles(), nil); err != nil {
		log.Printf("seed templates %s: %v", repoURL, err)
	}
}
