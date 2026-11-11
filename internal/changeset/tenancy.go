package changeset

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/validate"
	"github.com/epheo/dotvirt/pkg/forge"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// Tenant bootstrap: the one corner of the changeset that creates forge repos
// imperatively (a new project's repo, a lost repo's re-creation) before staging
// the declarative half into the platform draft. Everything else in this package
// only writes draft entries.

// LiveNamespaces reads what a namespace already carries on the cluster, nil
// when it does not exist. Bound to the caller's token by the handler, so the
// coordinator stays cluster-free and a user adopts exactly what they can read;
// the handler also refuses there a namespace the caller may not claim, since
// whether it exists decides what claiming it takes. A nil func means nothing
// exists yet.
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
func primaryNetworkOnExisting(ns string, live *netgen.LiveNamespace, net *netgen.PrimaryNet, joins bool) error {
	if live != nil && (net != nil || joins) {
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
	VMNetwork *netgen.PrimaryNet `json:"vmNetwork,omitempty"` // the first namespace's own
	// ProjectNetwork is the project's VM Network, spanning its namespaces and
	// its publish namespace (netgen.ProjectNetworkManifest).
	ProjectNetwork *netgen.PrimaryNet `json:"projectNetwork,omitempty"`
}

// StageCreateProject bootstraps a new tenant. The repo is created imperatively (a
// repo isn't a manifest), then the first namespace and - when owners are given - a
// RoleBinding granting them namespace-admin are staged into the PLATFORM repo
// (cluster-tenancy is admin-tier; a tenant repo couldn't carry either). commitProj
// is the platform project.
func (c *Coordinator) StageCreateProject(id auth.Identity, commitProj project.ProjectInfo, rawSpec json.RawMessage, live LiveNamespaces, exists ProjectExists) (model.DraftView, error) {
	spec, ns, err := parseProject(commitProj, rawSpec)
	if err != nil {
		return model.DraftView{}, err
	}
	// Whether the tenant already exists is a CLUSTER fact (exists), asked once
	// the gate passed: asked first, it would tell anyone which projects exist.
	// Refusing here on a pre-existing repo instead would burn the name: the
	// repo is created first, so a run that failed or was discarded after that
	// point could never be retried. ensureTenantRepo is idempotent, so a retry
	// reuses it. Read the namespace before the repo exists: a refusal must not
	// leave one behind.
	ln, err := live.lookup(ns)
	if err != nil {
		return model.DraftView{}, err
	}
	if err := primaryNetworkOnExisting(ns, ln, spec.VMNetwork, spec.ProjectNetwork != nil); err != nil {
		return model.DraftView{}, err
	}
	entries, repoURL, err := c.projectEntries(commitProj, spec, ns, ln)
	if err != nil {
		return model.DraftView{}, err
	}
	publishNS, err := c.publishNamespaceEntries(spec, live)
	if err != nil {
		return model.DraftView{}, err
	}
	entries = append(entries, publishNS...)
	// Gated before the repo exists: a refusal must not leave one behind.
	if err := c.gate(id, commitProj, entries); err != nil {
		return model.DraftView{}, err
	}
	if exists != nil && exists(spec.Name) {
		return model.DraftView{}, fmt.Errorf("%w: that project already exists; adopt it instead of creating it", model.ErrConflict)
	}
	created, err := c.ensureTenantRepo(repoURL)
	if err != nil {
		return model.DraftView{}, err
	}
	view, err := c.stage(id, commitProj, entries...)
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

// PreviewCreateProject renders what StageCreateProject would stage, through
// the same gate and the same existing-project refusal after it. It never
// calls the forge: the repo URL is derived, not created.
func (c *Coordinator) PreviewCreateProject(id auth.Identity, commitProj project.ProjectInfo, rawSpec json.RawMessage, live LiveNamespaces, exists ProjectExists) (model.StagePreview, error) {
	spec, ns, err := parseProject(commitProj, rawSpec)
	if err != nil {
		return model.StagePreview{}, err
	}
	ln, err := live.lookup(ns)
	if err != nil {
		return model.StagePreview{}, err
	}
	if err := primaryNetworkOnExisting(ns, ln, spec.VMNetwork, spec.ProjectNetwork != nil); err != nil {
		return model.StagePreview{}, err
	}
	entries, repoURL, err := c.projectEntries(commitProj, spec, ns, ln)
	if err != nil {
		return model.StagePreview{}, err
	}
	publishNS, err := c.publishNamespaceEntries(spec, live)
	if err != nil {
		return model.StagePreview{}, err
	}
	entries = append(entries, publishNS...)
	p, err := c.previewOf(id, commitProj, entries...)
	if err != nil {
		return p, err
	}
	if exists != nil && exists(spec.Name) {
		return model.StagePreview{}, fmt.Errorf("%w: that project already exists; adopt it instead of creating it", model.ErrConflict)
	}
	if _, err := c.tenantForge(repoURL); err != nil {
		return model.StagePreview{}, err
	}
	return p, nil
}

// ProjectExists reports whether a project by that name exists, the cluster
// being dotvirt's registry. nil reads none.
type ProjectExists func(name string) bool

// parseProject decodes and validates a new project's spec, with the name of
// its first namespace.
func parseProject(commitProj project.ProjectInfo, rawSpec json.RawMessage) (ProjectSpec, string, error) {
	if err := requireRepo(commitProj); err != nil {
		return ProjectSpec{}, "", err
	}
	var spec ProjectSpec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return ProjectSpec{}, "", fmt.Errorf("%w: invalid project spec: %v", model.ErrInvalid, err)
	}
	if spec.Name == "" {
		return ProjectSpec{}, "", fmt.Errorf("%w: a project name is required", model.ErrInvalid)
	}
	ns := spec.Namespace
	if ns == "" {
		ns = spec.Name
	}
	// The name becomes a repo path segment, a Namespace name, a label value, and a
	// staged manifest path - so it must be a strict DNS-1123 label. This rejects
	// path-traversal ("../x"), separators ("a/b"), and anything k8s would refuse.
	if err := tenantName(spec.Name); err != nil {
		return ProjectSpec{}, "", err
	}
	if err := validate.RequireDNS1123("namespace name", ns); err != nil {
		return ProjectSpec{}, "", err
	}
	return spec, ns, nil
}

// projectEntries is what creating project spec stages into commitProj: its
// first namespace ns, carrying ln when it exists, with its primary VM Network
// when the form chose one, and its grants. repoURL is the project's repo.
func (c *Coordinator) projectEntries(commitProj project.ProjectInfo, spec ProjectSpec, ns string, ln *netgen.LiveNamespace) ([]draft.Entry, string, error) {
	repoURL, err := tenantRepoURL(commitProj.Repo, spec.Name)
	if err != nil {
		return nil, "", err
	}
	first := []netgen.NamespaceSpec{{Name: ns, Project: spec.Name, Repo: forge.PathRef(repoURL), VMNetwork: spec.VMNetwork, ProjectNetwork: spec.ProjectNetwork != nil, Live: ln}}
	entries, err := c.projectAdoption(commitProj, first, spec.Owners)
	if err != nil || spec.ProjectNetwork == nil {
		return entries, repoURL, err
	}
	network, err := c.renderedEntry(commitProj, func() (draft.Entry, error) {
		path, content, err := netgen.ProjectNetworkManifest(spec.Name, *spec.ProjectNetwork)
		return draft.Entry{Resource: draft.ResourceNetwork, Name: spec.ProjectNetwork.Name, SourceFile: path, Manifest: string(content)}, err
	})
	if err != nil {
		return nil, "", err
	}
	// Rewriting a network git declares would move its namespaces onto this
	// project's.
	if network.Kind == draft.KindEdit {
		return nil, "", fmt.Errorf("%w: the platform repo declares a network named %s", model.ErrConflict, spec.ProjectNetwork.Name)
	}
	if other, err := c.declaredNetworkFor(commitProj, spec.Name); err != nil {
		return nil, "", err
	} else if other != "" {
		return nil, "", fmt.Errorf("%w: the platform repo's network %s already spans namespaces labeled for %s, an earlier project's: remove it first", model.ErrConflict, other, spec.Name)
	}
	return append(entries, network), repoURL, nil
}

// declaredNetworkFor names a ClusterUserDefinedNetwork commitProj declares
// that selects project's VM Network namespaces, "" for none: a second one
// would put the new project's namespaces on two primary networks.
func (c *Coordinator) declaredNetworkFor(commitProj project.ProjectInfo, project string) (string, error) {
	read, err := c.read(commitProj)
	if err != nil {
		return "", err
	}
	idx, err := indexDeclared(read, c.baseBranch)
	if err != nil {
		return "", err
	}
	for ref, path := range idx.Files {
		if ref.Kind != model.KindCUDN.Kind {
			continue
		}
		content, err := read.FileOnBranch(c.baseBranch, path)
		if err != nil {
			return "", err
		}
		if netgen.DeclaredNetworkSelects(content, project) {
			return ref.Name, nil
		}
	}
	return "", nil
}

// publishNamespaceEntries stages spec's publish namespace with the project,
// where the install publishes, on the project's VM Network when it has one.
// A first namespace with a VM Network of its own gets none: that network
// cannot span a publish namespace.
func (c *Coordinator) publishNamespaceEntries(spec ProjectSpec, live LiveNamespaces) ([]draft.Entry, error) {
	if !c.publishing.On() || spec.VMNetwork != nil {
		return nil, nil
	}
	return c.newPublishNamespace(spec.Name, spec.ProjectNetwork != nil, live)
}

// newPublishNamespace stages project's publish namespace. One the cluster
// holds for project already stays as it is; a namespace by that name held
// otherwise, a user's own project among them, is refused.
func (c *Coordinator) newPublishNamespace(project string, onNetwork bool, live LiveNamespaces) ([]draft.Entry, error) {
	name := tenancy.PublishNamespace(project)
	ln, err := live.lookup(name)
	if err != nil {
		return nil, err
	}
	if ln != nil {
		_, inProject := ln.Labels[c.publishing.ProjectLabel]
		if tenancy.PublishProject(name, ln.Labels, ln.Annotations, inProject) == project {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: namespace %s exists and is not %s's publish namespace", model.ErrConflict, name, project)
	}
	path, content, err := netgen.PublishNamespaceManifest(project, onNetwork)
	if err != nil {
		return nil, invalid(err)
	}
	return []draft.Entry{{
		Kind:       draft.KindCreate,
		Resource:   draft.ResourceNamespace,
		Namespace:  name,
		Name:       name,
		SourceFile: path,
		Manifest:   string(content),
	}}, nil
}

// StagePublishNamespace stages target's publish namespace into commitProj, for
// a project the cluster holds none for: one made before the install
// published. It joins the project's VM Network where the project has one,
// else the cluster's own network, which one of the project's namespaces must
// then be on.
func (c *Coordinator) StagePublishNamespace(id auth.Identity, commitProj, target project.ProjectInfo, pt PublishTarget, live LiveNamespaces) (model.DraftView, error) {
	entries, err := c.publishNamespacePlan(target, pt, live)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.stage(id, commitProj, entries...)
}

// PublishNamespaceNeeds is StagePublishNamespace's accesses, unstaged.
func (c *Coordinator) PublishNamespaceNeeds(commitProj, target project.ProjectInfo, pt PublishTarget) ([]Access, error) {
	entries, err := c.publishNamespacePlan(target, pt, nil)
	if err != nil {
		return nil, err
	}
	return c.needs(commitProj, entries)
}

func (c *Coordinator) publishNamespacePlan(target project.ProjectInfo, pt PublishTarget, live LiveNamespaces) ([]draft.Entry, error) {
	if !c.publishing.On() {
		return nil, fmt.Errorf("%w: publishing is not configured on this install", model.ErrUnavailable)
	}
	if err := tenantName(target.Name); err != nil {
		return nil, err
	}
	name := tenancy.PublishNamespace(target.Name)
	switch {
	case pt.PublishNamespace:
		return nil, fmt.Errorf("%w: %s exists", model.ErrConflict, name)
	case pt.PublishNameRequested:
		return nil, fmt.Errorf("%w: %s", ErrPublishNameRequested, name)
	case pt.PublishNameHeld:
		return nil, fmt.Errorf("%w: %s", ErrPublishNameHeld, name)
	case pt.ProjectNetwork && pt.NetworkResidue:
		return nil, fmt.Errorf("%w: namespaces outside %s are on its VM Network, which %s would join: remove them from it first", model.ErrConflict, target.Name, name)
	}
	if !pt.ProjectNetwork && !slices.ContainsFunc(target.Namespaces, func(ns string) bool { return cmp.Or(pt.Networks[ns], DefaultNetwork) == DefaultNetwork }) {
		return nil, fmt.Errorf("%w (%s)", ErrApart, name)
	}
	entries, err := c.newPublishNamespace(target.Name, pt.ProjectNetwork, live)
	if err == nil && len(entries) == 0 {
		err = fmt.Errorf("%w: %s exists", model.ErrConflict, name)
	}
	return entries, err
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
	if err := tenantName(target.Name); err != nil {
		return model.DraftView{}, err
	}
	if len(target.Namespaces) == 0 {
		return model.DraftView{}, fmt.Errorf("%w: project %q has no namespaces to adopt", model.ErrInvalid, target.Name)
	}
	// Read (and so gate) every namespace before any repo exists: a refusal must
	// not leave one behind.
	lives := make([]*netgen.LiveNamespace, 0, len(target.Namespaces))
	for _, ns := range target.Namespaces {
		ln, err := live.lookup(ns)
		if err != nil {
			return model.DraftView{}, err
		}
		lives = append(lives, ln)
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
			return model.DraftView{}, forgeFailed("repo for project "+target.Name, err)
		}
		if !c.forge.SameForge(target.Repo) {
			if !exists {
				// For drops the host: this was a probe by path. The real repo lives
				// elsewhere; never re-home it to a fresh empty one.
				return model.DraftView{}, fmt.Errorf("%w: project %q's repo (%s) is hosted on another forge", model.ErrConflict, target.Name, target.Repo)
			}
			// Re-home: no repo create, no seed; the repo is already here.
			entries, err := c.projectAdoption(commitProj, tenantNamespaces(target, target.Repo, lives), owners)
			if err != nil {
				return model.DraftView{}, err
			}
			return c.stage(id, commitProj, entries...)
		}
		if exists {
			return model.DraftView{}, fmt.Errorf("%w: project %q already has a repo (%s)", model.ErrConflict, target.Name, target.Repo)
		}
	}
	// The project is repoless, or its same-forge repo is genuinely lost;
	// ensureTenantRepo creates it (created is irrelevant on this path), once
	// the entries passed the gate.
	repoURL, err := tenantRepoURL(commitProj.Repo, target.Name)
	if err != nil {
		return model.DraftView{}, err
	}
	entries, err := c.projectAdoption(commitProj, tenantNamespaces(target, repoURL, lives), owners)
	if err != nil {
		return model.DraftView{}, err
	}
	if err := c.gate(id, commitProj, entries); err != nil {
		return model.DraftView{}, err
	}
	if _, err := c.ensureTenantRepo(repoURL); err != nil {
		return model.DraftView{}, err
	}
	return c.stage(id, commitProj, entries...)
}

// StageSyncAccess stages, into the platform draft, the grants of each of
// target's namespaces with its sync account added (grantSource.entries): the
// platform-repo PR a project created before sync impersonation needs before
// Argo may sync it as that account. Each grant keeps every subject it names.
func (c *Coordinator) StageSyncAccess(id auth.Identity, commitProj, target project.ProjectInfo) (model.DraftView, error) {
	entries, err := c.syncAccessEntries(commitProj, target)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.stage(id, commitProj, entries...)
}

// syncAccessEntries is what granting target's sync account stages into
// commitProj.
func (c *Coordinator) syncAccessEntries(commitProj, target project.ProjectInfo) ([]draft.Entry, error) {
	if c.syncNS == "" {
		return nil, fmt.Errorf("%w: no ArgoCD namespace is configured, so there is no sync account to grant", model.ErrInvalid)
	}
	if err := tenantName(target.Name); err != nil {
		return nil, err
	}
	grants, err := c.grantSource(commitProj)
	if err != nil {
		return nil, err
	}
	var entries []draft.Entry
	for _, ns := range target.Namespaces {
		grant, err := grants.entries(ns, target.Name, nil, nil)
		if err != nil {
			return nil, err
		}
		entries = append(entries, grant...)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: project %q already grants its sync account", model.ErrInvalid, target.Name)
	}
	return entries, nil
}

// ReleaseDeclared stages the declarative half of a project release: every
// project namespace the PLATFORM repo declares, wherever its file sits, is
// rewritten without its tenancy - the file stays (handing Argo a deletion
// would prune the namespace itself), only the project label and repo
// annotation go. Namespaces the platform repo does not describe come back as
// residue for the caller to unlabel imperatively (label residue has no git
// path); a declared one sent there would be relabeled by the next sync. A
// declared file carrying more than its Namespace (a VM Network rides some)
// refuses the whole release rather than pruning tenant networking. The
// project's sync account leaves its grants too (grantSource.released).
func (c *Coordinator) ReleaseDeclared(id auth.Identity, commitProj, target project.ProjectInfo) (staged, residue []string, err error) {
	entries, staged, residue, err := c.releaseEntries(commitProj, target)
	if err != nil {
		return nil, nil, err
	}
	if err := c.gate(id, commitProj, entries); err != nil {
		return nil, nil, err
	}
	if err := c.store.Stage(id.Username, commitProj.Name, entries...); err != nil {
		return nil, nil, err
	}
	return staged, residue, nil
}

// releaseEntries is what releasing target stages into commitProj, the names
// it stages, and the namespaces the platform repo does not declare.
func (c *Coordinator) releaseEntries(commitProj, target project.ProjectInfo) (entries []draft.Entry, staged, residue []string, err error) {
	read, err := c.read(commitProj)
	if err != nil {
		return nil, nil, nil, err
	}
	idx, err := indexDeclared(read, c.baseBranch)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, ns := range target.Namespaces {
		ref := model.ObjectRef{Kind: model.KindNS.Kind, Name: ns}
		path, ok := idx.Files[ref]
		if !ok {
			residue = append(residue, ns)
			continue
		}
		if _, serr := soleDeclarer(idx, []model.ObjectRef{ref}); serr != nil {
			return nil, nil, nil, fmt.Errorf(
				"%w: %s declares more than the namespace %s (e.g. a VM Network); rewriting it would prune those - release this project by editing the platform repo",
				model.ErrConflict, path, ns)
		}
		content, rerr := read.FileOnBranch(c.baseBranch, path)
		if rerr != nil {
			return nil, nil, nil, rerr
		}
		released, gerr := netgen.ReleasedNamespaceManifest(content)
		if gerr != nil {
			return nil, nil, nil, invalid(gerr)
		}
		e := draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourceNamespace,
			Namespace:  ns,
			Name:       ns,
			SourceFile: path,
			Manifest:   string(released),
			BaseBlob:   git.BlobID(content),
		}
		entries = append(entries, e)
		staged = append(staged, ns)
	}
	grants, err := c.grantSource(commitProj)
	if err != nil {
		return nil, nil, nil, err
	}
	revoked, err := grants.released(target)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, e := range revoked {
		entries = append(entries, e)
		staged = append(staged, e.Name)
	}
	return entries, staged, residue, nil
}

// tenantRepoURL is the tenant repo of project name: a sibling of the platform
// repo under the same owner.
func tenantRepoURL(platformRepo, name string) (string, error) {
	owner := forge.OwnerPrefixURL(platformRepo)
	if owner == platformRepo {
		return "", fmt.Errorf("%w: cannot derive a repo URL from the platform repo %q", model.ErrInvalid, platformRepo)
	}
	return owner + "/" + name + ".git", nil
}

// ensureTenantRepo creates the tenant repo on the forge when absent and seeds
// templates into a freshly created one. Shared by project creation and
// adoption; created is false when the repo already existed, which the caller
// uses to guard.
func (c *Coordinator) ensureTenantRepo(repoURL string) (created bool, err error) {
	fc, err := c.tenantForge(repoURL)
	if err != nil {
		return false, err
	}
	created, err = fc.EnsureRepo()
	if err != nil {
		// Only the create 404s: its owner organization is missing.
		return false, forgeFailed("organization for project repos", err)
	}
	if created {
		c.repos.Created(repoURL)
		c.seedTemplates(repoURL)
	}
	return created, nil
}

// tenantForge is the forge client that would create repoURL, without
// calling the forge: a project create refuses here, before any repo exists,
// when no forge is configured.
func (c *Coordinator) tenantForge(repoURL string) (*forge.Client, error) {
	fc := c.forge.For(repoURL)
	if fc == nil {
		return nil, fmt.Errorf("%w: forge not configured; cannot create the project repo", model.ErrInvalid)
	}
	return fc, nil
}

// tenantNamespaces is the namespace spec joining each of target's namespaces to
// repoURL: stamped with target's dotvirt.io/project label and the HOST-FREE
// dotvirt.io/repo annotation, so the forge identity lives only in the install
// config and a host change re-resolves projects instead of stranding them (the
// re-home path depends on it). Each keeps what it carries on the cluster, lives
// in target.Namespaces order.
func tenantNamespaces(target project.ProjectInfo, repoURL string, lives []*netgen.LiveNamespace) []netgen.NamespaceSpec {
	specs := make([]netgen.NamespaceSpec, 0, len(target.Namespaces))
	for i, ns := range target.Namespaces {
		specs = append(specs, netgen.NamespaceSpec{Name: ns, Project: target.Name, Repo: forge.PathRef(repoURL), Live: lives[i]})
	}
	return specs
}

// projectAdoption is the platform draft's half of creating, adopting and
// re-homing a project: each spec's Namespace manifest and grants
// (grantSource.entries). The Namespaces stage as creates the propose step writes
// by path - create-or-overwrite - so a namespace already in the platform repo
// (e.g. dotvirt-made, annotation later dropped) is corrected rather than
// duplicated.
func (c *Coordinator) projectAdoption(commitProj project.ProjectInfo, specs []netgen.NamespaceSpec, owners []string) ([]draft.Entry, error) {
	grants, err := c.grantSource(commitProj)
	if err != nil {
		return nil, err
	}
	return projectAdoption(specs, owners, grants)
}

func projectAdoption(specs []netgen.NamespaceSpec, owners []string, grants grantSource) ([]draft.Entry, error) {
	var entries []draft.Entry
	for _, spec := range specs {
		nsPath, nsContent, err := netgen.NamespaceManifest(spec)
		if err != nil {
			return nil, invalid(err)
		}
		entries = append(entries, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourceNamespace,
			Namespace:  spec.Name,
			Name:       spec.Name,
			SourceFile: nsPath,
			Manifest:   string(nsContent),
		})
		grant, err := grants.entries(spec.Name, spec.Project, owners, nil)
		if err != nil {
			return nil, err
		}
		entries = append(entries, grant...)
	}
	return entries, nil
}

// grantSource is what a project namespace's grants render from: the sync
// account namespace (Coordinator.WithSyncNamespace) and the grants the
// platform repo already declares.
type grantSource struct {
	syncNS string
	// declared is the base-branch file declaring ref, and its bytes; "" when
	// none does. sole refuses a file holding anything else, as one to rewrite.
	// nil reads none.
	declared func(ref model.ObjectRef, sole bool) (path string, content []byte, err error)
}

// grantSource reads commitProj's declared grants.
func (c *Coordinator) grantSource(commitProj project.ProjectInfo) (grantSource, error) {
	g := grantSource{syncNS: c.syncNS}
	read, err := c.read(commitProj)
	if err != nil {
		return g, err
	}
	idx, err := indexDeclared(read, c.baseBranch)
	if err != nil {
		return g, err
	}
	g.declared = func(ref model.ObjectRef, sole bool) (string, []byte, error) {
		path := idx.Files[ref]
		if sole {
			var err error
			if path, err = soleDeclarer(idx, []model.ObjectRef{ref}); err != nil {
				return "", nil, err
			}
		}
		if path == "" {
			return "", nil, nil
		}
		content, err := read.FileOnBranch(c.baseBranch, path)
		return path, content, err
	}
	return g, nil
}

// inherited are the subjects proj's declared admin-role grants name, its own
// and other projects' sync accounts aside: who a namespace joining proj grants
// admin, so its repo never holds there what its people do not. A narrower
// grant's subjects stay out: the joining grant would make them admins.
func (g grantSource) inherited(proj project.ProjectInfo) ([]netgen.Subject, error) {
	var out []netgen.Subject
	if g.declared == nil {
		return nil, nil
	}
	for _, ns := range proj.Namespaces {
		name := tenancy.AdminBinding(ns)
		path, content, err := g.declared(model.ObjectRef{Kind: model.KindRB.Kind, Namespace: ns, Name: name}, false)
		if err != nil {
			return nil, err
		}
		if path == "" {
			continue
		}
		role, subjects, _, err := netgen.DeclaredGrant(content, name)
		if err != nil {
			return nil, invalid(err)
		}
		if role != "admin" {
			continue
		}
		for _, sub := range subjects {
			if g.syncAccount(sub) || slices.Contains(out, sub) {
				continue
			}
			out = append(out, sub)
		}
	}
	return out, nil
}

// released stages proj's sync account out of each of its namespaces' grants:
// each rewritten without it, or removed when it named nothing else. Its name
// outlives the project, so a later project of that name inherits nothing.
func (g grantSource) released(proj project.ProjectInfo) ([]draft.Entry, error) {
	name, ok := tenancy.SyncAccount(proj.Name)
	if !ok || g.syncNS == "" || g.declared == nil {
		return nil, nil
	}
	account := *netgen.ServiceAccountSubject(g.syncNS, name)
	var out []draft.Entry
	for _, ns := range proj.Namespaces {
		for _, grant := range []struct {
			name   string
			render func(netgen.RoleBindingSpec) (string, []byte, error)
		}{
			{tenancy.AdminBinding(ns), netgen.RoleBindingManifest},
			{tenancy.EgressBinding(ns), netgen.EgressManifest},
			{tenancy.RoutesBinding(ns), netgen.RoutesManifest},
		} {
			path, content, err := g.declared(model.ObjectRef{Kind: model.KindRB.Kind, Namespace: ns, Name: grant.name}, true)
			if err != nil {
				return nil, err
			}
			if path == "" {
				continue
			}
			role, subjects, _, err := netgen.DeclaredGrant(content, grant.name)
			if err != nil {
				return nil, invalid(err)
			}
			if !slices.Contains(subjects, account) {
				continue
			}
			kept := slices.DeleteFunc(subjects, func(s netgen.Subject) bool { return s == account })
			e := draft.Entry{Kind: draft.KindDelete, Resource: draft.ResourceRoleBinding, Namespace: ns, Name: grant.name, SourceFile: path, BaseBlob: pinOf(content, true)}
			if len(kept) > 0 {
				_, manifest, err := grant.render(netgen.RoleBindingSpec{Namespace: ns, Role: role, Kept: kept})
				if err != nil {
					return nil, invalid(err)
				}
				e.Kind, e.Manifest = draft.KindEdit, string(manifest)
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// syncAccount reports whether sub is a project's sync account.
func (g grantSource) syncAccount(sub netgen.Subject) bool {
	return g.syncNS != "" && sub.Kind == "ServiceAccount" && sub.Namespace == g.syncNS && strings.HasPrefix(sub.Name, tenancy.AppProjectPrefix)
}

// entries stages ns's grants: the admin RoleBinding naming the subjects it
// already declares, inherit, the owners and the project's sync account
// (netgen.RoleBindingManifest), and, while that grant is the admin role, the
// EgressFirewall grant to those same subjects plus any it already declares
// (netgen.EgressManifest), and the HTTPRoute grant likewise where publishing
// declared one (netgen.RoutesManifest). Each rewrites the file declaring it,
// if any; the admin grant keeps its role. A grant git already states is left
// out, and none at all with nobody to grant.
func (g grantSource) entries(ns, proj string, owners []string, inherit []netgen.Subject) ([]draft.Entry, error) {
	spec := netgen.RoleBindingSpec{Namespace: ns, Project: proj, Owners: owners, Kept: inherit}
	if name, ok := tenancy.SyncAccount(proj); ok && g.syncNS != "" {
		spec.SyncAccount = netgen.ServiceAccountSubject(g.syncNS, name)
	}
	if spec.SyncAccount == nil && len(owners) == 0 && len(inherit) == 0 {
		return nil, nil
	}
	admin, err := g.entry(ns, tenancy.AdminBinding(ns), func(declared []byte) (string, []byte, error) {
		if declared != nil {
			role, kept, _, err := netgen.DeclaredGrant(declared, tenancy.AdminBinding(ns))
			if err != nil {
				return "", nil, err
			}
			spec.Role, spec.Kept = role, slices.Concat(kept, inherit)
		}
		return netgen.RoleBindingManifest(spec)
	})
	if err != nil {
		return nil, err
	}
	// Egress writes go to admins only: a project whose grant is a narrower
	// role holds none, and so neither does its repo.
	if spec.Role != "" && spec.Role != "admin" {
		return admin, nil
	}
	// After the admin grant: spec.Kept holds the subjects it declares.
	egress, err := g.follow(ns, tenancy.EgressBinding(ns), spec, netgen.EgressManifest, true)
	if err != nil {
		return nil, err
	}
	routes, err := g.follow(ns, tenancy.RoutesBinding(ns), spec, netgen.RoutesManifest, false)
	if err != nil {
		return nil, err
	}
	return slices.Concat(admin, egress, routes), nil
}

// follow stages RoleBinding name in ns, rendered by render for the admin
// grant's subjects (spec.Kept, once the admin grant read them) plus any it
// already declares. create=false stages nothing where git declares none.
func (g grantSource) follow(ns, name string, spec netgen.RoleBindingSpec, render func(netgen.RoleBindingSpec) (string, []byte, error), create bool) ([]draft.Entry, error) {
	if !create {
		if g.declared == nil {
			return nil, nil
		}
		path, _, err := g.declared(model.ObjectRef{Kind: model.KindRB.Kind, Namespace: ns, Name: name}, false)
		if err != nil || path == "" {
			return nil, err
		}
	}
	return g.entry(ns, name, func(declared []byte) (string, []byte, error) {
		s := spec
		if declared != nil {
			_, kept, _, err := netgen.DeclaredGrant(declared, name)
			if err != nil {
				return "", nil, err
			}
			s.Kept = slices.Concat(spec.Kept, kept)
		}
		return render(s)
	})
}

// routes stages ns's HTTPRoute grant to the subjects of its admin grant and
// the project's sync account: nothing while ns has no admin-role grant in
// git, as egress writes go to admins only.
func (g grantSource) routes(ns, proj string) ([]draft.Entry, error) {
	if g.declared == nil {
		return nil, nil
	}
	name := tenancy.AdminBinding(ns)
	path, content, err := g.declared(model.ObjectRef{Kind: model.KindRB.Kind, Namespace: ns, Name: name}, false)
	if err != nil || path == "" {
		return nil, err
	}
	role, subjects, _, err := netgen.DeclaredGrant(content, name)
	if err != nil {
		return nil, invalid(err)
	}
	if role != "admin" {
		return nil, nil
	}
	spec := netgen.RoleBindingSpec{Namespace: ns, Project: proj, Kept: subjects}
	if account, ok := tenancy.SyncAccount(proj); ok && g.syncNS != "" {
		spec.SyncAccount = netgen.ServiceAccountSubject(g.syncNS, account)
	}
	return g.follow(ns, tenancy.RoutesBinding(ns), spec, netgen.RoutesManifest, true)
}

// gatewayView stages the read of a project's Gateway, in its publish
// namespace publishNS, for the subjects of the admin grants of the
// namespaces it reaches, sync accounts aside: their repos hold nothing
// there. Nothing without such a grant in git: a provider binding its own
// groups there stages none here.
func (g grantSource) gatewayView(publishNS string, reach []string) ([]draft.Entry, error) {
	admins, err := g.inherited(project.ProjectInfo{Namespaces: reach})
	if err != nil || len(admins) == 0 {
		return nil, err
	}
	spec := netgen.RoleBindingSpec{Namespace: publishNS, Kept: admins}
	return g.follow(publishNS, tenancy.GatewayViewBinding, spec, netgen.GatewayViewManifest, true)
}

// entry stages RoleBinding ns/name as render renders it from the bytes git
// declares it in (nil for none): a create at its layout path, or an edit of
// its file pinned to those bytes; nothing when git already says the same.
func (g grantSource) entry(ns, name string, render func(declared []byte) (string, []byte, error)) ([]draft.Entry, error) {
	e := draft.Entry{Kind: draft.KindCreate, Resource: draft.ResourceRoleBinding, Namespace: ns, Name: name}
	var declared []byte
	if g.declared != nil {
		path, content, err := g.declared(model.ObjectRef{Kind: model.KindRB.Kind, Namespace: ns, Name: name}, true)
		if err != nil {
			return nil, err
		}
		if path != "" {
			declared = content
			e.Kind, e.SourceFile, e.BaseBlob = draft.KindEdit, path, pinOf(content, true)
		}
	}
	path, content, err := render(declared)
	if err != nil {
		return nil, invalid(err)
	}
	if declared != nil && manifest.SameDocument(declared, content) {
		return nil, nil
	}
	if e.SourceFile == "" {
		e.SourceFile = path
	}
	e.Manifest = string(content)
	return []draft.Entry{e}, nil
}

// tenantName validates a tenant project name: a DNS-1123 label that is not the
// platform tier's reserved name.
func tenantName(name string) error {
	if err := validate.RequireDNS1123("project name", name); err != nil {
		return err
	}
	if name == project.PlatformName {
		return fmt.Errorf("%w: %q is reserved for the platform repo", model.ErrInvalid, name)
	}
	// A project's first namespace defaults to its name.
	if strings.HasSuffix(name, tenancy.PublishSuffix) {
		return fmt.Errorf("%w: project names ending in %s are kept for publish namespaces", model.ErrInvalid, tenancy.PublishSuffix)
	}
	return nil
}
