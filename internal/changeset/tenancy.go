package changeset

import (
	"encoding/json"
	"fmt"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/validate"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Tenant bootstrap: the one corner of the changeset that creates forge repos
// imperatively (a new project's repo, a lost repo's re-creation) before staging
// the declarative half into the platform draft. Everything else in this package
// only writes draft entries.

// LiveNamespaces reads what a namespace already carries on the cluster, nil
// when it does not exist. Bound to the caller's token by the handler, so the
// coordinator stays cluster-free and a user adopts exactly what they can read.
// A nil func means nothing exists yet.
type LiveNamespaces func(name string) (*netgen.LiveNamespace, error)

func (l LiveNamespaces) lookup(name string) (*netgen.LiveNamespace, error) {
	if l == nil {
		return nil, nil
	}
	return l(name)
}

// primaryNetworkOnExisting refuses a VM Network on a namespace that already
// exists: OVN allows the primary-network label only at creation, so the sync
// could never apply it.
func primaryNetworkOnExisting(ns string, live *netgen.LiveNamespace, net *netgen.PrimaryNet) error {
	if live != nil && net != nil {
		return fmt.Errorf("%w: namespace %q already exists; a VM Network can only be added when a namespace is created", model.ErrConflict, ns)
	}
	return nil
}

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
func (c *Coordinator) StageCreateProject(id auth.Identity, commitProj project.ProjectInfo, rawSpec json.RawMessage, live LiveNamespaces) (model.DraftView, error) {
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
	if err := validate.RequireDNS1123("project name", spec.Name); err != nil {
		return model.DraftView{}, err
	}
	if err := validate.RequireDNS1123("namespace name", ns); err != nil {
		return model.DraftView{}, err
	}
	// Whether the tenant already exists is a CLUSTER fact, checked by the caller before
	// this runs. Refusing here on a pre-existing repo instead would burn the name: the
	// repo is created first, so a run that failed or was discarded after that point
	// could never be retried. ensureTenantRepo is idempotent, so a retry reuses it.
	// Read the namespace before the repo exists: a refusal must not leave one behind.
	ln, err := live.lookup(ns)
	if err != nil {
		return model.DraftView{}, err
	}
	if err := primaryNetworkOnExisting(ns, ln, spec.VMNetwork); err != nil {
		return model.DraftView{}, err
	}
	repoURL, created, err := c.ensureTenantRepo(commitProj.Repo, spec.Name)
	if err != nil {
		return model.DraftView{}, err
	}
	// The first namespace joins the new project, with its primary VM Network
	// when the form chose one.
	first := []netgen.NamespaceSpec{{Name: ns, Project: spec.Name, Repo: forge.PathRef(repoURL), VMNetwork: spec.VMNetwork, Live: ln}}
	if err := c.stageProjectAdoption(id.Username, commitProj.Name, first, spec.Owners); err != nil {
		return model.DraftView{}, err
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
// NotTracked and are brought in by adopting the namespace (AdoptObjects) - still
// PR-gated. commitProj is the platform project; target is the project being adopted.
func (c *Coordinator) AdoptProject(id auth.Identity, commitProj, target project.ProjectInfo, owners []string, live LiveNamespaces) (model.DraftView, error) {
	if err := requireRepo(commitProj); err != nil {
		return model.DraftView{}, err
	}
	if err := validate.RequireDNS1123("project name", target.Name); err != nil {
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
			specs, err := tenantNamespaces(target, target.Repo, live)
			if err != nil {
				return model.DraftView{}, err
			}
			if err := c.stageProjectAdoption(id.Username, commitProj.Name, specs, owners); err != nil {
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
	specs, err := tenantNamespaces(target, repoURL, live)
	if err != nil {
		return model.DraftView{}, err
	}
	if err := c.stageProjectAdoption(id.Username, commitProj.Name, specs, owners); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, commitProj)
}

// ReleaseDeclared stages the declarative half of a project release: every
// project namespace the PLATFORM repo declares is rewritten without its
// tenancy - the file and whatever else it carries stay (handing Argo a
// deletion would prune the namespace itself), only the project label and repo
// annotation go. Namespaces the
// platform repo does not describe come back as residue for the caller to
// unlabel imperatively (label residue has no git path). A declared file
// carrying more than its Namespace (a VM Network rides some) refuses the whole
// release rather than pruning tenant networking.
func (c *Coordinator) ReleaseDeclared(id auth.Identity, commitProj, target project.ProjectInfo) (staged, residue []string, err error) {
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
		released, gerr := netgen.ReleasedNamespaceManifest(content)
		if gerr != nil {
			return nil, nil, invalid(gerr)
		}
		if serr := c.store.Stage(id.Username, commitProj.Name, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourceNamespace,
			Namespace:  ns,
			Name:       ns,
			SourceFile: path,
			Manifest:   string(released),
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
	owner := forge.OwnerPrefixURL(platformRepo)
	if owner == platformRepo {
		return "", false, fmt.Errorf("%w: cannot derive a repo URL from the platform repo %q", model.ErrInvalid, platformRepo)
	}
	repoURL = owner + "/" + name + ".git"
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

// tenantNamespaces is the namespace spec joining each of target's namespaces to
// repoURL: stamped with target's dotvirt.io/project label and the HOST-FREE
// dotvirt.io/repo annotation, so the forge identity lives only in the install
// config and a host change re-resolves projects instead of stranding them (the
// re-home path depends on it). Each keeps what it already carries on the cluster.
func tenantNamespaces(target project.ProjectInfo, repoURL string, live LiveNamespaces) ([]netgen.NamespaceSpec, error) {
	specs := make([]netgen.NamespaceSpec, 0, len(target.Namespaces))
	for _, ns := range target.Namespaces {
		ln, err := live.lookup(ns)
		if err != nil {
			return nil, err
		}
		specs = append(specs, netgen.NamespaceSpec{Name: ns, Project: target.Name, Repo: forge.PathRef(repoURL), Live: ln})
	}
	return specs, nil
}

// stageProjectAdoption stages the Namespace manifest of each spec (and, when
// owners are given, the namespace-admin RoleBinding that makes them a tenant)
// into commitProjName's platform draft: the declarative half of creating,
// adopting and re-homing a project, unit-testable without a forge. Staged as
// creates that the propose step writes by path - create-or-overwrite - so a
// namespace already in the platform repo (e.g. dotvirt-made, annotation later
// dropped) is corrected rather than duplicated.
func (c *Coordinator) stageProjectAdoption(username, commitProjName string, specs []netgen.NamespaceSpec, owners []string) error {
	for _, spec := range specs {
		nsPath, nsContent, err := netgen.NamespaceManifest(spec)
		if err != nil {
			return invalid(err)
		}
		if err := c.store.Stage(username, commitProjName, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourceNamespace,
			Namespace:  spec.Name,
			Name:       spec.Name,
			SourceFile: nsPath,
			Manifest:   string(nsContent),
		}); err != nil {
			return err
		}
		if len(owners) == 0 {
			continue
		}
		rbPath, rbContent, err := netgen.RoleBindingManifest(netgen.RoleBindingSpec{Namespace: spec.Name, Project: spec.Project, Owners: owners})
		if err != nil {
			return invalid(err)
		}
		if err := c.store.Stage(username, commitProjName, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourceRoleBinding,
			Namespace:  spec.Name,
			Name:       spec.Name + "-admins",
			SourceFile: rbPath,
			Manifest:   string(rbContent),
		}); err != nil {
			return err
		}
	}
	return nil
}
