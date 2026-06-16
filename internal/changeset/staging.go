package changeset

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/vmgen"
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
	if spec.Name == "" || spec.Namespace == "" {
		return model.DraftView{}, fmt.Errorf("%w: name and namespace are required", model.ErrInvalid)
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

// StageCreateNetwork records a new Distributed Port Group in (id, proj)'s draft:
// a namespace-scoped UDN (project scope) or a cluster-scoped CUDN (shared/vlan
// scope) — for the latter, proj is the platform repo (dotvirt routes cluster-scoped
// creates there by KIND, not an admin-picked repo). The rendered manifest is staged
// verbatim (the adopt-create path) so propose commits it and Argo applies it on merge.
func (c *Coordinator) StageCreateNetwork(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	var spec netgen.Spec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: invalid network spec: %v", model.ErrInvalid, err)
	}
	path, content, err := netgen.Manifest(spec)
	if err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	ns := spec.Namespace
	if ns == "" {
		ns = ClusterScopeNS // cluster-scoped CUDN
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindCreate,
		Resource:   draft.ResourceNetwork,
		Namespace:  ns,
		Name:       spec.Name,
		SourceFile: path,
		Manifest:   string(content),
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// StageCreateNamespace records a new namespace (with an optional primary "VM
// Network"). A Namespace is cluster-scoped, so it is COMMITTED to commitProj (the
// platform repo) and applied by the platform Argo app — but it is labeled/annotated
// to joinProj, the tenant project it JOINS, so that project's per-project app syncs
// workloads into it once it exists. The namespace + primary UDN land as one
// multi-doc manifest.
func (c *Coordinator) StageCreateNamespace(id auth.Identity, commitProj, joinProj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	if err := requireRepo(commitProj); err != nil {
		return model.DraftView{}, err
	}
	if joinProj.Repo == "" {
		return model.DraftView{}, fmt.Errorf("%w: the joining project has no repo", model.ErrInvalid)
	}
	var spec netgen.NamespaceSpec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: invalid namespace spec: %v", model.ErrInvalid, err)
	}
	// Stamp the namespace's dotvirt.io labels/annotations to the tenant it joins,
	// not the platform repo it's committed to.
	spec.Project, spec.Repo = joinProj.Name, joinProj.Repo
	path, content, err := netgen.NamespaceManifest(spec)
	if err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if err := c.store.Stage(id.Username, commitProj.Name, draft.Entry{
		Kind:       draft.KindCreate,
		Resource:   draft.ResourceNamespace,
		Namespace:  spec.Name,
		Name:       spec.Name,
		SourceFile: path,
		Manifest:   string(content),
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, commitProj)
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
	if !validName(spec.Name) {
		return model.DraftView{}, fmt.Errorf("%w: project name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", model.ErrInvalid, spec.Name)
	}
	if !validName(ns) {
		return model.DraftView{}, fmt.Errorf("%w: namespace name %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", model.ErrInvalid, ns)
	}
	// The new tenant repo is a sibling of the platform repo under the same owner.
	repoURL := siblingRepoURL(commitProj.Repo, spec.Name)
	if repoURL == "" {
		return model.DraftView{}, fmt.Errorf("%w: cannot derive a repo URL from the platform repo %q", model.ErrInvalid, commitProj.Repo)
	}
	fc := c.forge.For(repoURL)
	if fc == nil {
		return model.DraftView{}, fmt.Errorf("%w: forge not configured; cannot create the project repo", model.ErrInvalid)
	}
	if _, err := fc.EnsureRepo(); err != nil {
		return model.DraftView{}, fmt.Errorf("create project repo: %w", err)
	}
	// First namespace, joined to the new project/repo (stamps its dotvirt.io labels).
	nsSpec := netgen.NamespaceSpec{Name: ns, Project: spec.Name, Repo: repoURL, VMNetwork: spec.VMNetwork}
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
	return c.Get(id, commitProj)
}

// dns1123Label matches a single RFC-1123 label: lowercase alphanumeric and '-',
// starting and ending alphanumeric. Length is checked separately.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// validName reports whether s is a safe project/namespace name — a DNS-1123 label.
// This is the trust boundary for a name that becomes a repo path, a Namespace, a
// label value, and a git file path, so it rejects traversal and separators.
func validName(s string) bool {
	return len(s) > 0 && len(s) <= 63 && dns1123Label.MatchString(s)
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
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	var spec netgen.UplinkSpec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: invalid uplink spec: %v", model.ErrInvalid, err)
	}
	path, content, err := netgen.UplinkManifest(spec)
	if err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindCreate,
		Resource:   draft.ResourceUplink,
		Namespace:  ClusterScopeNS,
		Name:       spec.Name,
		SourceFile: path,
		Manifest:   string(content),
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
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
// from (id, proj)'s draft.
func (c *Coordinator) Unstage(id auth.Identity, proj project.ProjectInfo, resource, namespace, name string) error {
	return c.store.Unstage(id.Username, proj.Name, draft.Resource(resource), namespace, name)
}

// Discard clears (id, proj)'s draft.
func (c *Coordinator) Discard(id auth.Identity, proj project.ProjectInfo) error {
	return c.store.Clear(id.Username, proj.Name)
}
