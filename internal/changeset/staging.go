package changeset

import (
	"encoding/json"
	"fmt"

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
