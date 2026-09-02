package changeset

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Tenant bootstrap: the one corner of the changeset that creates forge repos
// imperatively (a new project's repo, a lost repo's re-creation) before staging
// the declarative half into the platform draft. Everything else in this package
// only writes draft entries.

// ProjectSpec describes a new tenant project to bootstrap from the UI: a forge repo,
// a first namespace (optionally with a primary VM Network), and the owners granted
// admin on it. This is what fills the "no New Project button" gap.
type ProjectSpec struct {
	Name      string             `json:"name"`      // project name -> repo name + dotvirt.io/project
	Namespace string             `json:"namespace"` // first namespace; defaults to Name
	Owners    []string           `json:"owners,omitempty"`
	VMNetwork *netgen.PrimaryNet `json:"vmNetwork,omitempty"`
}

// StageCreateProject bootstraps a new tenant. The repo is created imperatively (a
// repo isn't a manifest), then the first namespace and - when owners are given - a
// RoleBinding granting them namespace-admin are staged into the PLATFORM repo
// (cluster-tenancy is admin-tier; a tenant repo couldn't carry either). commitProj
// is the platform project.
func (c *Coordinator) StageCreateProject(id auth.Identity, commitProj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	if err := requireRepo(commitProj); err != nil {
		return model.DraftView{}, err
	}
	var spec ProjectSpec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: invalid project spec: %v", model.ErrInvalid, err)
	}
	if spec.Name == "" {
		return model.DraftView{}, fmt.Errorf("%w: a project name is required", model.ErrInvalid)
	}
	ns := spec.Namespace
	if ns == "" {
		ns = spec.Name
	}
	// The name becomes a repo path segment, a Namespace name, a label value, and a
	// staged manifest path - so it must be a strict DNS-1123 label. This rejects
	// path-traversal ("../x"), separators ("a/b"), and anything k8s would refuse.
	if err := requireDNS1123("project name", spec.Name); err != nil {
		return model.DraftView{}, err
	}
	if err := requireDNS1123("namespace name", ns); err != nil {
		return model.DraftView{}, err
	}
	// Whether the tenant already exists is a CLUSTER fact, checked by the caller before
	// this runs. Refusing here on a pre-existing repo instead would burn the name: the
	// repo is created first, so a run that failed or was discarded after that point
	// could never be retried. ensureTenantRepo is idempotent, so a retry reuses it.
	repoURL, created, err := c.ensureTenantRepo(commitProj.Repo, spec.Name)
	if err != nil {
		return model.DraftView{}, err
	}
	// First namespace, joined to the new project/repo (stamps its dotvirt.io labels).
	// Host-free ref: the forge identity lives only in the install config, so a
	// host change re-resolves projects instead of stranding them.
	nsSpec := netgen.NamespaceSpec{Name: ns, Project: spec.Name, Repo: forge.PathRef(repoURL), VMNetwork: spec.VMNetwork}
	nsPath, nsContent, err := netgen.NamespaceManifest(nsSpec)
	if err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if err := c.store.Stage(id.Username, commitProj.Name, draft.Entry{
		Kind:       draft.KindCreate,
		Resource:   draft.ResourceNamespace,
		Namespace:  ns,
		Name:       ns,
		SourceFile: nsPath,
		Manifest:   string(nsContent),
	}); err != nil {
		return model.DraftView{}, err
	}
	// Owners -> a namespace-admin RoleBinding (the delegation that makes it a tenant).
	if len(spec.Owners) > 0 {
		rbPath, rbContent, err := netgen.RoleBindingManifest(netgen.RoleBindingSpec{
			Namespace: ns, Project: spec.Name, Owners: spec.Owners,
		})
		if err != nil {
			return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
		}
		if err := c.store.Stage(id.Username, commitProj.Name, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourceRoleBinding,
			Namespace:  ns,
			Name:       ns + "-admins",
			SourceFile: rbPath,
			Manifest:   string(rbContent),
		}); err != nil {
			return model.DraftView{}, err
		}
	}
	view, err := c.Get(id, commitProj)
	if err != nil {
		return view, err
	}
	if !created {
		// A retry reuses its own repo; a former install's leftover deploys its
		// contents on merge with nothing in the PR showing them. Only the human can
		// tell the two apart, so say it.
		view.Warning = JoinWarning(view.Warning,
			fmt.Sprintf("Reusing the existing repo %s: whatever it currently holds deploys once this project lands.", repoURL))
	}
	return view, nil
}

// AdoptProject wires a repo to an EXISTING labeled-but-repoless project - the
// read-only "no repo configured" dead-end the inventory shows. It mirrors
// StageCreateProject but targets a project that already exists in the cluster: the
// tenant repo is created imperatively, then each of the project's namespaces is
// (re-)staged into the PLATFORM repo carrying the dotvirt.io/repo annotation. On
// merge the namespaces come under Argo and the ApplicationSet generates the project's
// app (it skips repoless projects). VMs in those namespaces then surface as
// NotTracked and are brought in via AdoptNamespace - still PR-gated. commitProj is
// the platform project; target is the project being adopted.
func (c *Coordinator) AdoptProject(id auth.Identity, commitProj, target project.ProjectInfo, owners []string) (model.DraftView, error) {
	if err := requireRepo(commitProj); err != nil {
		return model.DraftView{}, err
	}
	if err := requireDNS1123("project name", target.Name); err != nil {
		return model.DraftView{}, err
	}
	if len(target.Namespaces) == 0 {
		return model.DraftView{}, fmt.Errorf("%w: project %q has no namespaces to adopt", model.ErrInvalid, target.Name)
	}
	// A resolving annotation means already managed: conflict. Two dead ends recover:
	// the forge LOST the repo (re-create; safe, allowEmpty refuses an empty source),
	// or the HOST changed while the repo survived under the same owner/name
	// (RE-HOME: stage the host-free namespace manifests; the PR is the re-point).
	// A repo this forge cannot speak for stays a conflict.
	if target.Repo != "" {
		fc := c.forge.For(target.Repo)
		if fc == nil {
			return model.DraftView{}, fmt.Errorf("%w: project %q already has a repo (%s)", model.ErrConflict, target.Name, target.Repo)
		}
		exists, err := fc.RepoExists()
		if err != nil {
			return model.DraftView{}, fmt.Errorf("check project repo: %w", err)
		}
		if !c.forge.SameForge(target.Repo) {
			if !exists {
				// For drops the host: this was a probe by path. The real repo lives
				// elsewhere; never re-home it to a fresh empty one.
				return model.DraftView{}, fmt.Errorf("%w: project %q's repo (%s) is hosted on another forge", model.ErrConflict, target.Name, target.Repo)
			}
			// Re-home: no repo create, no seed; the repo is already here.
			if err := c.stageProjectAdoption(id.Username, commitProj.Name, target, target.Repo, owners); err != nil {
				return model.DraftView{}, err
			}
			return c.Get(id, commitProj)
		}
		if exists {
			return model.DraftView{}, fmt.Errorf("%w: project %q already has a repo (%s)", model.ErrConflict, target.Name, target.Repo)
		}
	}
	// The project is repoless, or its same-forge repo is genuinely lost;
	// ensureTenantRepo creates it (created is irrelevant on this path).
	repoURL, _, err := c.ensureTenantRepo(commitProj.Repo, target.Name)
	if err != nil {
		return model.DraftView{}, err
	}
	if err := c.stageProjectAdoption(id.Username, commitProj.Name, target, repoURL, owners); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, commitProj)
}

// ReleaseDeclared stages the declarative half of a project release: every
// project namespace the PLATFORM repo declares is rewritten as a plain
// Namespace - the file stays (handing Argo a deletion would prune the
// namespace itself), the project label and repo annotation go. Namespaces the
// platform repo does not describe come back as residue for the caller to
// unlabel imperatively (label residue has no git path). A declared file
// carrying more than its Namespace (a VM Network rides some) refuses the whole
// release rather than pruning tenant networking.
func (c *Coordinator) ReleaseDeclared(id auth.Identity, commitProj, target project.ProjectInfo) (staged, residue []string, err error) {
	if err := requireRepo(commitProj); err != nil {
		return nil, nil, err
	}
	read, err := c.read(commitProj)
	if err != nil {
		return nil, nil, err
	}
	for _, ns := range target.Namespaces {
		path := "namespaces/" + ns + ".yaml"
		content, ok, lerr := read.LookupOnBranch(c.baseBranch, path)
		if lerr != nil {
			return nil, nil, lerr
		}
		if !ok {
			residue = append(residue, ns)
			continue
		}
		refs := git.DeclaredRefs(path, content)
		if len(refs) != 1 || refs[0].Kind != "Namespace" {
			return nil, nil, fmt.Errorf(
				"%w: %s declares more than the namespace %s (e.g. a VM Network); rewriting it would prune those - release this project by editing the platform repo",
				model.ErrConflict, path, ns)
		}
		pPath, pContent, gerr := netgen.PlainNamespaceManifest(ns)
		if gerr != nil {
			return nil, nil, fmt.Errorf("%w: %v", model.ErrInvalid, gerr)
		}
		if serr := c.store.Stage(id.Username, commitProj.Name, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourceNamespace,
			Namespace:  ns,
			Name:       ns,
			SourceFile: pPath,
			Manifest:   string(pContent),
		}); serr != nil {
			return nil, nil, serr
		}
		staged = append(staged, ns)
	}
	return staged, residue, nil
}

// ensureTenantRepo derives the tenant repo URL - a sibling of the platform repo
// under the same owner - creates it on the forge when absent, and seeds templates
// into a freshly created one. Shared by project creation and adoption; created is
// false when the repo already existed, which the caller uses to guard.
func (c *Coordinator) ensureTenantRepo(platformRepo, name string) (repoURL string, created bool, err error) {
	repoURL = siblingRepoURL(platformRepo, name)
	if repoURL == "" {
		return "", false, fmt.Errorf("%w: cannot derive a repo URL from the platform repo %q", model.ErrInvalid, platformRepo)
	}
	fc := c.forge.For(repoURL)
	if fc == nil {
		return "", false, fmt.Errorf("%w: forge not configured; cannot create the project repo", model.ErrInvalid)
	}
	created, err = fc.EnsureRepo()
	if err != nil {
		return "", false, fmt.Errorf("create project repo: %w", err)
	}
	if created {
		c.seedTemplates(repoURL)
	}
	return repoURL, created, nil
}

// stageProjectAdoption stages the namespace (+ optional owner RoleBinding) manifests
// that join target to repoURL into commitProjName's platform draft. Split from
// AdoptProject so the staging is unit-testable without a forge. Each namespace
// manifest is stamped with target's dotvirt.io/project label and dotvirt.io/repo
// annotation (netgen.NamespaceManifest), staged as a create that the propose step
// writes by path - create-or-overwrite - so a namespace already in the platform repo
// (e.g. dotvirt-made, annotation later dropped) is corrected rather than duplicated.
func (c *Coordinator) stageProjectAdoption(username, commitProjName string, target project.ProjectInfo, repoURL string, owners []string) error {
	for _, ns := range target.Namespaces {
		// Host-free ref; the re-home path depends on it.
		nsPath, nsContent, err := netgen.NamespaceManifest(netgen.NamespaceSpec{Name: ns, Project: target.Name, Repo: forge.PathRef(repoURL)})
		if err != nil {
			return fmt.Errorf("%w: %v", model.ErrInvalid, err)
		}
		if err := c.store.Stage(username, commitProjName, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourceNamespace,
			Namespace:  ns,
			Name:       ns,
			SourceFile: nsPath,
			Manifest:   string(nsContent),
		}); err != nil {
			return err
		}
		if len(owners) == 0 {
			continue
		}
		rbPath, rbContent, err := netgen.RoleBindingManifest(netgen.RoleBindingSpec{Namespace: ns, Project: target.Name, Owners: owners})
		if err != nil {
			return fmt.Errorf("%w: %v", model.ErrInvalid, err)
		}
		if err := c.store.Stage(username, commitProjName, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourceRoleBinding,
			Namespace:  ns,
			Name:       ns + "-admins",
			SourceFile: rbPath,
			Manifest:   string(rbContent),
		}); err != nil {
			return err
		}
	}
	return nil
}

// siblingRepoURL derives a repo URL alongside ref under the same owner: it replaces
// ref's last path segment with name (.../<owner>/<ref>.git -> .../<owner>/<name>.git).
func siblingRepoURL(ref, name string) string {
	s := strings.TrimSuffix(strings.TrimRight(ref, "/"), ".git")
	i := strings.LastIndexByte(s, '/')
	if i < 0 {
		return ""
	}
	return s[:i+1] + name + ".git"
}
