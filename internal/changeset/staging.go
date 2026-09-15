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
	"github.com/epheo/dotvirt/internal/vmgen"
	"github.com/epheo/dotvirt/pkg/forge"
)

// StageEdit records a VM edit in (id, proj)'s draft.
func (c *Coordinator) StageEdit(id auth.Identity, proj project.ProjectInfo, namespace, name string, req model.EditRequest) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	edit := req.VMEdit
	if edit.Empty() {
		return model.DraftView{}, fmt.Errorf("%w: no fields to edit", model.ErrInvalid)
	}
	// SourceFile addresses a file in the proposal diff - the one repo path a
	// client supplies directly, so it passes the same gate created names do.
	if err := validate.RequireRepoPath("source file", req.SourceFile); err != nil {
		return model.DraftView{}, err
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

// StageCreateVM records a new VM in (id, proj)'s draft: the wizard spec rendered
// to its manifest here, once, so the preview shows the bytes propose commits and
// a spec the renderer refuses fails at the form rather than at propose.
func (c *Coordinator) StageCreateVM(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	var spec vmgen.Spec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: invalid VM spec: %v", model.ErrInvalid, err)
	}
	path, content, err := vmgen.Manifest(spec)
	if err != nil {
		return model.DraftView{}, invalid(err)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindCreate,
		Namespace:  spec.Namespace,
		Name:       spec.Name,
		SourceFile: path,
		Manifest:   string(content),
		FromWizard: true,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// stageRendered is the shared tail of StageCreate and StageCreateNamespace:
// requireRepo, then one rendered manifest staged verbatim (the adopt-create path)
// so propose commits it and Argo applies it on merge. requireRepo runs before
// render so a repoless project fails ErrConflict, never ErrInvalid; any render
// error (spec decode included) is the caller's input, classified ErrInvalid.
// render returns the entry minus Kind; the entry's Namespace is the object's own
// or the model.ClusterScopeNS sentinel.
//
// The rendered manifest is a create when git does not yet declare its objects
// and an edit - the same rendering replacing the declaring file - when it does:
// one path for the form that creates an object and the form that changes it.
func (c *Coordinator) stageRendered(id auth.Identity, proj project.ProjectInfo, render func() (draft.Entry, error)) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	entry, err := render()
	if err != nil {
		return model.DraftView{}, invalid(err)
	}
	entry.Kind = draft.KindCreate
	idx, err := read.DeclaredFilesOnBranch(c.baseBranch)
	if err != nil {
		return model.DraftView{}, err
	}
	if path, err := soleDeclarer(idx, git.DeclaredRefs(entry.SourceFile, []byte(entry.Manifest))); err != nil {
		return model.DraftView{}, err
	} else if path != "" {
		entry.Kind, entry.SourceFile = draft.KindEdit, path
	}
	if err := c.store.Stage(id.Username, proj.Name, entry); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// soleDeclarer is the base-branch file declaring exactly refs, or "" when git
// declares none of them. Anything in between is a conflict to resolve in git:
// refs split across files, a file also holding other objects (a namespace
// beside its primary network) or documents the index cannot name. A rewrite
// or removal acts on the whole file, so it is offered only when the file is
// nothing but the objects asked for.
func soleDeclarer(idx git.DeclaredIndex, refs []model.ObjectRef) (string, error) {
	path := ""
	for _, ref := range refs {
		if p, ok := idx.Files[ref]; ok && path != "" && p != path {
			return "", fmt.Errorf("%w: %s is declared across several files; edit it in git", model.ErrConflict, ref.Name)
		} else if ok {
			path = p
		}
	}
	if path == "" {
		return "", nil
	}
	want := make(map[model.ObjectRef]bool, len(refs))
	for _, ref := range refs {
		if _, ok := idx.Files[ref]; !ok {
			return "", fmt.Errorf("%w: %s is declared in %s beside objects not in this change; edit it in git", model.ErrConflict, ref.Name, path)
		}
		want[ref] = true
	}
	for ref, p := range idx.Files {
		if p == path && !want[ref] {
			return "", fmt.Errorf("%w: %s/%s is declared in %s beside other objects; edit it in git", model.ErrConflict, ref.Namespace, ref.Name, path)
		}
	}
	if idx.Opaque[path] {
		return "", fmt.Errorf("%w: %s holds documents beside its declared objects; edit it in git", model.ErrConflict, path)
	}
	return path, nil
}

// renderer turns a create form's spec into the entry it stages: the manifest
// netgen renders and the identity the draft, the object routes and the Changes
// pane carry.
type renderer func(rawSpec json.RawMessage) (draft.Entry, error)

// renderers is the network family's create forms, keyed by the resource a
// route names. The route resolved the target tier from the object's scope; a
// renderer decides only how the spec renders and what identity it stages under.
var renderers = map[draft.Resource]renderer{
	// A namespace-scoped UDN (project scope) or a cluster-scoped CUDN
	// (shared/vlan scope), the latter under the cluster sentinel.
	draft.ResourceNetwork: render("network", netgen.Manifest, func(s netgen.Spec) (draft.Resource, string, string) {
		return draft.ResourceNetwork, model.DraftNamespace(s.Namespace), s.Name
	}),
	// An uplink's identity is its NNCP's name, which the read plane and the
	// object routes carry; the physical-network name is what it maps.
	draft.ResourceUplink: render("uplink", netgen.UplinkManifest, func(s netgen.UplinkSpec) (draft.Resource, string, string) {
		return draft.ResourceUplink, model.ClusterScopeNS, netgen.UplinkPolicyName(s.Name)
	}),
	// OVN-K permits one egress firewall per namespace, always named "default".
	draft.ResourceEgressFirewall: render("egress firewall", netgen.EgressFirewallManifest, func(s netgen.EgressFirewallSpec) (draft.Resource, string, string) {
		return draft.ResourceEgressFirewall, s.Namespace, "default"
	}),
	draft.ResourceEgressIP: render("egress IP", netgen.EgressIPManifest, func(s netgen.EgressIPSpec) (draft.Resource, string, string) {
		return draft.ResourceEgressIP, model.ClusterScopeNS, s.Name
	}),
	draft.ResourceExternalRoute: render("external route", netgen.ExternalRouteManifest, func(s netgen.ExternalRouteSpec) (draft.Resource, string, string) {
		return draft.ResourceExternalRoute, model.ClusterScopeNS, s.Name
	}),
	draft.ResourceNetworkPolicy: render("network policy", netgen.NetworkPolicyManifest, func(s netgen.NetworkPolicySpec) (draft.Resource, string, string) {
		return draft.ResourceNetworkPolicy, s.Namespace, s.Name
	}),
	// One form serves both admin tiers: the spec's baseline flag picks the
	// singleton BaselineAdminNetworkPolicy, always named "default".
	draft.ResourceAdminNetworkPolicy:         adminPolicy,
	draft.ResourceBaselineAdminNetworkPolicy: adminPolicy,
}

var adminPolicy = render("admin network policy", netgen.AdminNetworkPolicyManifest, func(s netgen.AdminNetworkPolicySpec) (draft.Resource, string, string) {
	if s.Baseline {
		return draft.ResourceBaselineAdminNetworkPolicy, model.ClusterScopeNS, "default"
	}
	return draft.ResourceAdminNetworkPolicy, model.ClusterScopeNS, s.Name
})

// render builds a renderer: decode rawSpec into S, render its manifest, name
// the entry. meta derives the identity from the decoded spec (the per-kind
// quirks live there). A free function because methods cannot take type
// parameters.
func render[S any](what string, manifest func(S) (path string, content []byte, err error), meta func(S) (resource draft.Resource, ns, name string)) renderer {
	return func(rawSpec json.RawMessage) (draft.Entry, error) {
		var spec S
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return draft.Entry{}, fmt.Errorf("invalid %s spec: %v", what, err)
		}
		path, content, err := manifest(spec)
		resource, ns, name := meta(spec)
		return draft.Entry{
			Resource:   resource,
			Namespace:  ns,
			Name:       name,
			SourceFile: path,
			Manifest:   string(content),
		}, err
	}
}

// StageCreate records a new network-family object in (id, proj)'s draft, its
// form spec rendered to a manifest here, once. The route resolved proj from
// the object's scope - the tenant repo owning a namespaced object, the
// platform repo for a cluster-scoped one - so resource only picks the renderer.
func (c *Coordinator) StageCreate(id auth.Identity, proj project.ProjectInfo, resource draft.Resource, rawSpec json.RawMessage) (model.DraftView, error) {
	stage, ok := renderers[resource]
	if !ok {
		return model.DraftView{}, fmt.Errorf("%w: %s has no create form", model.ErrInvalid, resource)
	}
	return c.stageRendered(id, proj, func() (draft.Entry, error) { return stage(rawSpec) })
}

// StageCreateNamespace records a new namespace (with an optional primary "VM
// Network"). A Namespace is cluster-scoped, so it is COMMITTED to commitProj (the
// platform repo) and applied by the platform Argo app - but it is labeled/annotated
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

// StageDelete records the removal of an existing object (a VM when resource is
// empty) in (id, proj)'s draft. The object must be declared on the base branch
// (you can't delete what isn't in git - an unstaged create should be unstaged,
// not deleted); its file is captured so the propose step removes it and Argo
// prunes the object on merge. Cluster-scoped objects name model.ClusterScopeNS.
func (c *Coordinator) StageDelete(id auth.Identity, proj project.ProjectInfo, resource, namespace, name string) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	path, err := c.locate(read, draft.Resource(resource), namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindDelete,
		Resource:   draft.Resource(resource),
		Namespace:  namespace,
		Name:       name,
		SourceFile: path,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// Unstage removes one pending change (of the given resource - empty means VM)
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
