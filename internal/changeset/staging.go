package changeset

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/validate"
	"github.com/epheo/dotvirt/internal/vmgen"
	"github.com/epheo/dotvirt/pkg/forge"
)

// StageEdit records a VM edit in (id, proj)'s draft.
func (c *Coordinator) StageEdit(id auth.Identity, proj project.ProjectInfo, namespace, name string, req model.EditRequest) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	edit := editFromRequest(req)
	if edit.Empty() {
		return model.DraftView{}, fmt.Errorf("%w: no fields to edit", model.ErrInvalid)
	}
	// SourceFile addresses a file in the proposal diff — the one repo path a
	// client supplies directly, so it passes the same gate created names do.
	if err := validate.RequireRepoPath("source file", req.SourceFile); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindEdit,
		Namespace:  namespace,
		Name:       name,
		SourceFile: req.SourceFile,
		Edit:       &edit,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// StageCreate records a new-VM spec in (id, proj)'s draft.
func (c *Coordinator) StageCreate(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	var spec vmgen.Spec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: invalid VM spec: %v", model.ErrInvalid, err)
	}
	// The pair becomes the manifest's repo path (ns/name.yaml); reject traversal
	// and non-DNS-1123 names at stage time, not at propose.
	if err := validate.RequireDNS1123("VM name", spec.Name); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if err := validate.RequireDNS1123("namespace", spec.Namespace); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:      draft.KindCreate,
		Namespace: spec.Namespace,
		Name:      spec.Name,
		Spec:      &spec,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// ClusterScopeNS is the placeholder "namespace" for cluster-scoped draft entries
// (CUDN networks, NNCP uplinks) — they have no real namespace, but the draft keys
// + the unstage route are ns/name-shaped, so a sentinel keeps both well-formed.
const ClusterScopeNS = "cluster"

// stageRendered is the shared tail of the netgen-backed StageCreateX methods:
// requireRepo, then one rendered manifest staged verbatim (the adopt-create path)
// so propose commits it and Argo applies it on merge. requireRepo runs before
// render so a repoless project fails ErrConflict, never ErrInvalid; any render
// error (spec decode included) is the caller's input, wrapped as ErrInvalid.
// render returns the entry minus Kind; the entry's Namespace is the object's own
// or the ClusterScopeNS sentinel.
func (c *Coordinator) stageRendered(id auth.Identity, proj project.ProjectInfo, render func() (draft.Entry, error)) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	entry, err := render()
	if err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	entry.Kind = draft.KindCreate
	if err := c.store.Stage(id.Username, proj.Name, entry); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// StageCreateNetwork records a new Distributed Port Group in (id, proj)'s draft:
// a namespace-scoped UDN (project scope) or a cluster-scoped CUDN (shared/vlan
// scope) — for the latter, proj is the platform repo (dotvirt routes cluster-scoped
// creates there by KIND, not an admin-picked repo).
func (c *Coordinator) StageCreateNetwork(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return c.stageRendered(id, proj, func() (draft.Entry, error) {
		var spec netgen.Spec
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return draft.Entry{}, fmt.Errorf("invalid network spec: %v", err)
		}
		path, content, err := netgen.Manifest(spec)
		ns := spec.Namespace
		if ns == "" {
			ns = ClusterScopeNS // cluster-scoped CUDN
		}
		return draft.Entry{
			Resource:   draft.ResourceNetwork,
			Namespace:  ns,
			Name:       spec.Name,
			SourceFile: path,
			Manifest:   string(content),
		}, err
	})
}

// StageCreateNamespace records a new namespace (with an optional primary "VM
// Network"). A Namespace is cluster-scoped, so it is COMMITTED to commitProj (the
// platform repo) and applied by the platform Argo app — but it is labeled/annotated
// to joinProj, the tenant project it JOINS, so that project's per-project app syncs
// workloads into it once it exists. The namespace + primary UDN land as one
// multi-doc manifest.
func (c *Coordinator) StageCreateNamespace(id auth.Identity, commitProj, joinProj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return c.stageRendered(id, commitProj, func() (draft.Entry, error) {
		if joinProj.Repo == "" {
			return draft.Entry{}, fmt.Errorf("the joining project has no repo")
		}
		var spec netgen.NamespaceSpec
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return draft.Entry{}, fmt.Errorf("invalid namespace spec: %v", err)
		}
		// Stamp the namespace's dotvirt.io labels/annotations to the tenant it joins,
		// not the platform repo it's committed to. Host-free ref ONLY when the repo is
		// on this forge: stripping a genuinely foreign host would re-point the project.
		ref := joinProj.Repo
		if c.forge.SameForge(ref) {
			ref = forge.PathRef(ref)
		}
		spec.Project, spec.Repo = joinProj.Name, ref
		path, content, err := netgen.NamespaceManifest(spec)
		return draft.Entry{
			Resource:   draft.ResourceNamespace,
			Namespace:  spec.Name,
			Name:       spec.Name,
			SourceFile: path,
			Manifest:   string(content),
		}, err
	})
}

// ProjectSpec describes a new tenant project to bootstrap from the UI: a forge repo,
// a first namespace (optionally with a primary VM Network), and the owners granted
// admin on it. This is what fills the "no New Project button" gap.
type ProjectSpec struct {
	Name      string             `json:"name"`      // project name → repo name + dotvirt.io/project
	Namespace string             `json:"namespace"` // first namespace; defaults to Name
	Owners    []string           `json:"owners,omitempty"`
	VMNetwork *netgen.PrimaryNet `json:"vmNetwork,omitempty"`
}

// StageCreateProject bootstraps a new tenant. The repo is created imperatively (a
// repo isn't a manifest), then the first namespace and — when owners are given — a
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
	// staged manifest path — so it must be a strict DNS-1123 label. This rejects
	// path-traversal ("../x"), separators ("a/b"), and anything k8s would refuse.
	if !validate.DNS1123Name(spec.Name) {
		return model.DraftView{}, fmt.Errorf("%w: project name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", model.ErrInvalid, spec.Name)
	}
	if !validate.DNS1123Name(ns) {
		return model.DraftView{}, fmt.Errorf("%w: namespace name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", model.ErrInvalid, ns)
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
	// The ref is stamped HOST-FREE (owner/repo.git): the forge's identity lives only
	// in the install config, so a forge-host change re-resolves every project instead
	// of stranding them on a dead absolute URL.
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
	// Owners → a namespace-admin RoleBinding (the delegation that makes it a tenant).
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
		// The retry path above legitimately reuses the repo, but so would a leftover from
		// a former install, and THAT repo's current contents deploy the moment this PR
		// merges and the ApplicationSet wires it, with nothing in the PR showing them.
		// Only the human can tell the two apart, so say it rather than decide it.
		view.Warning = JoinWarning(view.Warning,
			fmt.Sprintf("Reusing the existing repo %s: whatever it currently holds deploys once this project lands.", repoURL))
	}
	return view, nil
}

// AdoptProject wires a repo to an EXISTING labeled-but-repoless project — the
// read-only "no repo configured" dead-end the inventory shows. It mirrors
// StageCreateProject but targets a project that already exists in the cluster: the
// tenant repo is created imperatively, then each of the project's namespaces is
// (re-)staged into the PLATFORM repo carrying the dotvirt.io/repo annotation. On
// merge the namespaces come under Argo and the ApplicationSet generates the project's
// app (it skips repoless projects). VMs in those namespaces then surface as
// NotTracked and are brought in via AdoptNamespace — still PR-gated. commitProj is
// the platform project; target is the project being adopted.
func (c *Coordinator) AdoptProject(id auth.Identity, commitProj, target project.ProjectInfo, owners []string) (model.DraftView, error) {
	if err := requireRepo(commitProj); err != nil {
		return model.DraftView{}, err
	}
	if !validate.DNS1123Name(target.Name) {
		return model.DraftView{}, fmt.Errorf("%w: project name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", model.ErrInvalid, target.Name)
	}
	if len(target.Namespaces) == 0 {
		return model.DraftView{}, fmt.Errorf("%w: project %q has no namespaces to adopt", model.ErrInvalid, target.Name)
	}
	// An annotation that still resolves means the project is already managed. One that
	// does not is a dead end this recovers, in one of two shapes. The forge LOST the
	// repo: re-create it (safe: ArgoCD's automated.allowEmpty defaults to false, so an
	// empty source is refused, never applied). The forge HOST changed (a reinstall, an
	// apps-domain move) while the repo itself survived under the same owner/name:
	// RE-HOME it by staging the namespace manifests, whose refs are stamped host-free,
	// so the reviewable platform PR is exactly the re-point. Only a repo this forge
	// cannot speak for at all stays a conflict.
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
				// For keeps only the owner/repo, so this forge was merely PROBED by path; the
				// project's real repo lives elsewhere and must not be re-homed to a fresh
				// empty one here.
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

// ensureTenantRepo derives the tenant repo URL — a sibling of the platform repo
// under the same owner — creates it on the forge when absent, and seeds templates
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
// writes by path — create-or-overwrite — so a namespace already in the platform repo
// (e.g. dotvirt-made, annotation later dropped) is corrected rather than duplicated.
func (c *Coordinator) stageProjectAdoption(username, commitProjName string, target project.ProjectInfo, repoURL string, owners []string) error {
	for _, ns := range target.Namespaces {
		// Host-free ref, like StageCreateProject: this is also what makes the re-home
		// path work at all — the staged manifest re-points the project by construction.
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
// ref's last path segment with name (…/<owner>/<ref>.git → …/<owner>/<name>.git).
func siblingRepoURL(ref, name string) string {
	s := strings.TrimSuffix(strings.TrimRight(ref, "/"), ".git")
	i := strings.LastIndexByte(s, '/')
	if i < 0 {
		return ""
	}
	return s[:i+1] + name + ".git"
}

// StageCreateUplink records a new Uplink (an nmstate NNCP) in (id, proj)'s draft —
// proj is the platform repo (an uplink is cluster-scoped, so it always routes to the
// platform tier). Stages under the ClusterScopeNS sentinel.
func (c *Coordinator) StageCreateUplink(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return c.stageRendered(id, proj, func() (draft.Entry, error) {
		var spec netgen.UplinkSpec
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return draft.Entry{}, fmt.Errorf("invalid uplink spec: %v", err)
		}
		path, content, err := netgen.UplinkManifest(spec)
		return draft.Entry{
			Resource:   draft.ResourceUplink,
			Namespace:  ClusterScopeNS,
			Name:       spec.Name,
			SourceFile: path,
			Manifest:   string(content),
		}, err
	})
}

// StageCreateEgressFirewall records a new namespace egress firewall (the Tier-1
// gateway firewall) in (id, proj)'s draft. It is namespace-scoped, so proj is the
// tenant project owning the namespace (handlers route it via resolveProject, like a
// project UDN). The object is always named "default" (OVN-K permits one per namespace).
func (c *Coordinator) StageCreateEgressFirewall(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return c.stageRendered(id, proj, func() (draft.Entry, error) {
		var spec netgen.EgressFirewallSpec
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return draft.Entry{}, fmt.Errorf("invalid egress firewall spec: %v", err)
		}
		path, content, err := netgen.EgressFirewallManifest(spec)
		return draft.Entry{
			Resource:   draft.ResourceEgressFirewall,
			Namespace:  spec.Namespace,
			Name:       "default",
			SourceFile: path,
			Manifest:   string(content),
		}, err
	})
}

// StageCreateEgressIP records a new cluster-scoped EgressIP (the Tier-0 source-NAT
// pool) in (id, proj)'s draft. proj is the platform repo — cluster-scoped, so it
// always routes to the platform tier (handlers gate it on the caller's EgressIP-create
// authority). Staged under the ClusterScopeNS sentinel.
func (c *Coordinator) StageCreateEgressIP(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return c.stageRendered(id, proj, func() (draft.Entry, error) {
		var spec netgen.EgressIPSpec
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return draft.Entry{}, fmt.Errorf("invalid egress IP spec: %v", err)
		}
		path, content, err := netgen.EgressIPManifest(spec)
		return draft.Entry{
			Resource:   draft.ResourceEgressIP,
			Namespace:  ClusterScopeNS,
			Name:       spec.Name,
			SourceFile: path,
			Manifest:   string(content),
		}, err
	})
}

// StageCreateExternalRoute records a new cluster-scoped AdminPolicyBasedExternalRoute
// (the Tier-0 external next-hop route) in (id, proj)'s draft — proj is the platform
// repo. Staged under the ClusterScopeNS sentinel.
func (c *Coordinator) StageCreateExternalRoute(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return c.stageRendered(id, proj, func() (draft.Entry, error) {
		var spec netgen.ExternalRouteSpec
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return draft.Entry{}, fmt.Errorf("invalid external route spec: %v", err)
		}
		path, content, err := netgen.ExternalRouteManifest(spec)
		return draft.Entry{
			Resource:   draft.ResourceExternalRoute,
			Namespace:  ClusterScopeNS,
			Name:       spec.Name,
			SourceFile: path,
			Manifest:   string(content),
		}, err
	})
}

// StageCreateNetworkPolicy records a new NetworkPolicy (the east-west Distributed
// Firewall) in (id, proj)'s draft — namespace-scoped, so proj is the tenant project
// owning the namespace.
func (c *Coordinator) StageCreateNetworkPolicy(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return c.stageRendered(id, proj, func() (draft.Entry, error) {
		var spec netgen.NetworkPolicySpec
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return draft.Entry{}, fmt.Errorf("invalid network policy spec: %v", err)
		}
		path, content, err := netgen.NetworkPolicyManifest(spec)
		return draft.Entry{
			Resource:   draft.ResourceNetworkPolicy,
			Namespace:  spec.Namespace,
			Name:       spec.Name,
			SourceFile: path,
			Manifest:   string(content),
		}, err
	})
}

// StageCreateAdminNetworkPolicy records a new cluster-wide admin DFW policy (the
// AdminNetworkPolicy that overrides tenant NetworkPolicies, or the singleton
// BaselineAdminNetworkPolicy default) in (id, proj)'s draft. proj is the platform
// repo — cluster-scoped + admin-only, so it always routes to the platform tier
// (handlers gate it on the caller's ANP/BANP-create authority). Staged under the
// ClusterScopeNS sentinel.
func (c *Coordinator) StageCreateAdminNetworkPolicy(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	return c.stageRendered(id, proj, func() (draft.Entry, error) {
		var spec netgen.AdminNetworkPolicySpec
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return draft.Entry{}, fmt.Errorf("invalid admin network policy spec: %v", err)
		}
		path, content, err := netgen.AdminNetworkPolicyManifest(spec)
		resource, name := draft.ResourceAdminNetworkPolicy, spec.Name
		if spec.Baseline {
			resource, name = draft.ResourceBaselineAdminNetworkPolicy, "default"
		}
		return draft.Entry{
			Resource:   resource,
			Namespace:  ClusterScopeNS,
			Name:       name,
			SourceFile: path,
			Manifest:   string(content),
		}, err
	})
}

// StageDelete records the removal of an existing VM in (id, proj)'s draft. The VM
// must exist on the base branch (you can't delete what isn't in git — an unstaged
// create should be unstaged, not deleted); its manifest path is captured so the
// propose step removes that file and Argo prunes the VM on merge.
func (c *Coordinator) StageDelete(id auth.Identity, proj project.ProjectInfo, namespace, name string) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	vm, ok, err := read.FindVMOnBranch(c.baseBranch, namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	if !ok {
		return model.DraftView{}, fmt.Errorf("%w: %s/%s not on %s", model.ErrNotFound, namespace, name, c.baseBranch)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindDelete,
		Namespace:  namespace,
		Name:       name,
		SourceFile: vm.SourceFile,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// Unstage removes one pending change (of the given resource — empty means VM)
// from (id, proj)'s draft. An atomic resource unstages as a whole set: its
// entries are one logical change, so removing a single file from under it
// would leave a proposable half-change.
func (c *Coordinator) Unstage(id auth.Identity, proj project.ProjectInfo, resource, namespace, name string) error {
	if r := draft.Resource(resource); r.Atomic() {
		_, err := c.unstageResource(id, proj, r)
		return err
	}
	return c.store.Unstage(id.Username, proj.Name, draft.Resource(resource), namespace, name)
}

// Discard clears (id, proj)'s draft.
func (c *Coordinator) Discard(id auth.Identity, proj project.ProjectInfo) error {
	return c.store.Clear(id.Username, proj.Name)
}
