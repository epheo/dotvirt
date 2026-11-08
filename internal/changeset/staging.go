package changeset

import (
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
	"github.com/epheo/dotvirt/internal/vmgen"
	"github.com/epheo/dotvirt/pkg/forge"
)

// StageEdit records a VM edit in (id, proj)'s draft, on top of any edit
// already staged for the VM (see stageEdit).
func (c *Coordinator) StageEdit(id auth.Identity, proj project.ProjectInfo, namespace, name string, req model.EditRequest) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	edit := req.VMEdit
	if edit.Empty() {
		return model.DraftView{}, fmt.Errorf("%w: no fields to edit", model.ErrInvalid)
	}
	// req.SourceFile is ignored: stageEdit resolves the file from git.
	return c.stageEdit(id, proj, draft.Entry{
		Kind:      draft.KindEdit,
		Namespace: namespace,
		Name:      name,
		Edit:      &edit,
	})
}

// vmFile is the base-branch file declaring a VM, read once: the edit merged
// over what is staged, its dry run and the entry all work from these bytes.
type vmFile struct {
	path    string
	content []byte
	vm      model.VM
}

// editTarget resolves the file declaring VM (namespace, name) from git's
// declared index, never from the client, so an edit cannot be aimed at
// another namespace's manifest. It runs before the draft store's lock: the
// index walk on a memo miss must not hold every user's staging. The index
// and the read may see different heads; a file that moved in between is a
// conflict to retry, never an internal error.
func (c *Coordinator) editTarget(read *git.Repo, namespace, name string) (vmFile, error) {
	idx, err := indexDeclared(read, c.baseBranch)
	if err != nil {
		return vmFile{}, err
	}
	ref, ok := declaredRef(idx, draft.ResourceVM, namespace, name)
	if !ok {
		return vmFile{}, fmt.Errorf("%w: %s/%s is not in git", model.ErrNotFound, namespace, name)
	}
	path := idx.Files[ref]
	if files := idx.Repeated[ref]; len(files) > 0 {
		return vmFile{}, fmt.Errorf("%w: %s/%s is declared in %s; keep one declaration in git", model.ErrConflict, namespace, name, strings.Join(files, " and "))
	}
	content, ok, err := read.LookupOnBranch(c.baseBranch, path)
	if err != nil {
		return vmFile{}, err
	}
	retry := fmt.Errorf("%w: %s changed while this was staged; try again", model.ErrConflict, path)
	if !ok {
		return vmFile{}, retry
	}
	if err := namedVM(path, content, namespace, name); err != nil {
		return vmFile{}, err
	}
	vms, err := manifest.ParseVMs(path, content, defaultNamespace(path))
	if err != nil {
		return vmFile{}, fmt.Errorf("%w: %s does not parse; fix it in git: %v", model.ErrConflict, path, err)
	}
	i := slices.IndexFunc(vms, func(vm model.VM) bool { return vm.Namespace == namespace && vm.Name == name })
	switch {
	case i < 0:
		return vmFile{}, retry
	case slices.ContainsFunc(vms[i+1:], func(vm model.VM) bool { return vm.Namespace == namespace && vm.Name == name }):
		return vmFile{}, fmt.Errorf("%w: %s declares %s/%s twice; keep one declaration in git", model.ErrConflict, path, namespace, name)
	}
	return vmFile{path: path, content: content, vm: vms[i]}, nil
}

// namedVM refuses a VM document that leaves its namespace to the directory:
// dotvirt reads it there, but Argo applies it to its Application's namespace,
// which the edit's authorization never checked.
func namedVM(path string, content []byte, namespace, name string) error {
	ref := model.ObjectRef{Kind: model.KindVM.Kind, Namespace: namespace, Name: name}
	if _, implied, _ := declared(path, content); slices.Contains(implied, ref) {
		return fmt.Errorf("%w: %s does not set metadata.namespace for VM %s/%s; set it in git", model.ErrConflict, path, namespace, name)
	}
	return nil
}

// PreviewEdit renders an edit as the changes staging it would list, restart
// flags included, without touching the draft: an inline editor states the
// consequence, or the value staging would refuse, before the user stages.
func (c *Coordinator) PreviewEdit(proj project.ProjectInfo, namespace, name string, edit model.VMEdit) ([]model.Change, error) {
	if err := requireRepo(proj); err != nil {
		return nil, err
	}
	if err := manifest.ValidateEdit(edit); err != nil {
		return nil, err
	}
	read, err := c.read(proj)
	if err != nil {
		return nil, err
	}
	current, ok, err := findVM(read, c.baseBranch, namespace, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s/%s is not in git", model.ErrNotFound, namespace, name)
	}
	changes := manifest.ChangesForEdit(current, edit)
	if changes == nil {
		changes = []model.Change{}
	}
	return changes, nil
}

// StageCreateVM records a new VM in (id, proj)'s draft: the wizard spec rendered
// to its manifest here, once, so the preview shows the bytes propose commits and
// a spec the renderer refuses fails at the form rather than at propose.
func (c *Coordinator) StageCreateVM(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	entry, err := c.renderVM(proj, rawSpec)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.stage(id, proj, entry)
}

// PreviewCreateVM is StageCreateVM without the store write.
func (c *Coordinator) PreviewCreateVM(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.StagePreview, error) {
	entry, err := c.renderVM(proj, rawSpec)
	if err != nil {
		return model.StagePreview{}, err
	}
	return c.previewOf(id, proj, entry)
}

func (c *Coordinator) renderVM(proj project.ProjectInfo, rawSpec json.RawMessage) (draft.Entry, error) {
	read, err := c.read(proj)
	if err != nil {
		return draft.Entry{}, err
	}
	var spec vmgen.Spec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return draft.Entry{}, fmt.Errorf("%w: invalid VM spec: %v", model.ErrInvalid, err)
	}
	path, content, err := vmgen.Manifest(spec)
	if err != nil {
		return draft.Entry{}, invalid(err)
	}
	if err := c.requireUndeclaredVM(read, spec.Namespace, spec.Name); err != nil {
		return draft.Entry{}, err
	}
	return draft.Entry{
		Kind:       draft.KindCreate,
		Namespace:  spec.Namespace,
		Name:       spec.Name,
		SourceFile: path,
		Manifest:   string(content),
		FromWizard: true,
	}, nil
}

// previewOf is stage up to the store write: the same gate (the caller's RBAC
// for every object the files touch, the placement guard, Argo's reading of
// the files) and the same check against the caller's draft, then what the
// entries would write. Nothing is recorded.
func (c *Coordinator) previewOf(id auth.Identity, proj project.ProjectInfo, entries ...draft.Entry) (model.StagePreview, error) {
	if err := c.gate(id, proj, entries); err != nil {
		return model.StagePreview{}, err
	}
	staged, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		return model.StagePreview{}, err
	}
	d := make(map[string]draft.Entry, len(staged))
	for _, e := range staged {
		d[e.Key()] = e
	}
	if err := overFieldEdits(d, entries); err != nil {
		return model.StagePreview{}, err
	}
	return preview(proj, entries...), nil
}

// preview lists what staging entries into proj's draft would write.
func preview(proj project.ProjectInfo, entries ...draft.Entry) model.StagePreview {
	p := model.StagePreview{Project: proj.Name, Files: make([]model.PreviewFile, 0, len(entries))}
	for _, e := range entries {
		p.Files = append(p.Files, model.PreviewFile{Kind: string(e.Kind), Path: e.SourceFile, YAML: e.Manifest})
	}
	return p
}

// requireUndeclaredVM refuses a create of a VM the base branch already
// declares: merging would silently replace it (a duplicate deploy, a wizard
// name collision). The declared index answers, since the VM may sit in a
// multi-document file at another path; a failed read is a failure, never
// absence.
func (c *Coordinator) requireUndeclaredVM(read *git.Repo, namespace, name string) error {
	idx, err := indexDeclared(read, c.baseBranch)
	if err != nil {
		return err
	}
	if ref, ok := declaredRef(idx, draft.ResourceVM, namespace, name); ok {
		return fmt.Errorf("%w: %s/%s already exists in git (%s)", model.ErrConflict, namespace, name, idx.Files[ref])
	}
	return nil
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
	entry, err := c.renderedEntry(proj, render)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.stage(id, proj, entry)
}

// previewRendered is stageRendered without the store write.
func (c *Coordinator) previewRendered(id auth.Identity, proj project.ProjectInfo, render func() (draft.Entry, error)) (model.StagePreview, error) {
	entry, err := c.renderedEntry(proj, render)
	if err != nil {
		return model.StagePreview{}, err
	}
	return c.previewOf(id, proj, entry)
}

// renderedEntry is stageRendered's entry, unstaged.
func (c *Coordinator) renderedEntry(proj project.ProjectInfo, render func() (draft.Entry, error)) (draft.Entry, error) {
	read, err := c.read(proj)
	if err != nil {
		return draft.Entry{}, err
	}
	entry, err := render()
	if err != nil {
		return draft.Entry{}, invalid(err)
	}
	entry.Kind = draft.KindCreate
	idx, err := indexDeclared(read, c.baseBranch)
	if err != nil {
		return draft.Entry{}, err
	}
	if path, err := soleDeclarer(idx, declaredRefs(entry.SourceFile, []byte(entry.Manifest))); err != nil {
		return draft.Entry{}, err
	} else if path != "" {
		entry.Kind, entry.SourceFile = draft.KindEdit, path
	}
	return entry, nil
}

// soleDeclarer is the base-branch file declaring exactly refs, or "" when git
// declares none of them. Anything in between is a conflict to resolve in git:
// refs split across files, a file also holding other objects (a namespace
// beside its primary network) or documents the index cannot name. A rewrite
// or removal acts on the whole file, so it is offered only when the file is
// nothing but the objects asked for.
func soleDeclarer(idx declaredIndex, refs []model.ObjectRef) (string, error) {
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
	render, err := createForm(resource, rawSpec)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.stageRendered(id, proj, render)
}

// PreviewCreate is StageCreate without the store write.
func (c *Coordinator) PreviewCreate(id auth.Identity, proj project.ProjectInfo, resource draft.Resource, rawSpec json.RawMessage) (model.StagePreview, error) {
	render, err := createForm(resource, rawSpec)
	if err != nil {
		return model.StagePreview{}, err
	}
	return c.previewRendered(id, proj, render)
}

func createForm(resource draft.Resource, rawSpec json.RawMessage) (func() (draft.Entry, error), error) {
	r, ok := renderers[resource]
	if !ok {
		return nil, fmt.Errorf("%w: %s has no create form", model.ErrInvalid, resource)
	}
	return func() (draft.Entry, error) { return r(rawSpec) }, nil
}

// StageCreateNamespace records a new namespace (with an optional primary "VM
// Network"). A Namespace is cluster-scoped, so it is COMMITTED to commitProj (the
// platform repo) and applied by the platform Argo app - but it is labeled/annotated
// to joinProj, the tenant project it JOINS, so that project's per-project app syncs
// workloads into it once it exists. The namespace + primary UDN land as one
// multi-doc manifest, beside its grants (grantSource.entries) to the joining
// project's people, as its other namespaces' grants name them, and its sync
// account when one is configured: the project syncs into every namespace it
// holds, as no more than its people.
func (c *Coordinator) StageCreateNamespace(id auth.Identity, commitProj, joinProj project.ProjectInfo, rawSpec json.RawMessage, live LiveNamespaces) (model.DraftView, error) {
	entries, err := c.namespaceEntries(commitProj, joinProj, rawSpec, live)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.stage(id, commitProj, entries...)
}

// PreviewCreateNamespace is StageCreateNamespace without the store write:
// the namespace and its grants, through the same gate.
func (c *Coordinator) PreviewCreateNamespace(id auth.Identity, commitProj, joinProj project.ProjectInfo, rawSpec json.RawMessage, live LiveNamespaces) (model.StagePreview, error) {
	entries, err := c.namespaceEntries(commitProj, joinProj, rawSpec, live)
	if err != nil {
		return model.StagePreview{}, err
	}
	return c.previewOf(id, commitProj, entries...)
}

// namespaceEntries is what a new namespace stages: its manifest, then its
// grants.
func (c *Coordinator) namespaceEntries(commitProj, joinProj project.ProjectInfo, rawSpec json.RawMessage, live LiveNamespaces) ([]draft.Entry, error) {
	var spec netgen.NamespaceSpec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return nil, invalid(fmt.Errorf("invalid namespace spec: %v", err))
	}
	// An existing namespace joins as it is; only a new one may take a VM Network.
	ln, err := live.lookup(spec.Name)
	if err != nil {
		return nil, err
	}
	if err := primaryNetworkOnExisting(spec.Name, ln, spec.VMNetwork); err != nil {
		return nil, err
	}
	spec.Live = ln
	entry, err := c.renderedEntry(commitProj, func() (draft.Entry, error) {
		if joinProj.Repo == "" {
			return draft.Entry{}, fmt.Errorf("the joining project has no repo")
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
	if err != nil {
		return nil, err
	}
	grants, err := c.grantSource(commitProj)
	if err != nil {
		return nil, err
	}
	// The joining project's people come with it: its sync account never
	// holds a namespace they do not.
	inherit, err := grants.inherited(joinProj)
	if err != nil {
		return nil, err
	}
	grant, err := grants.entries(spec.Name, joinProj.Name, nil, inherit)
	if err != nil {
		return nil, err
	}
	return append([]draft.Entry{entry}, grant...), nil
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
	return c.stage(id, proj, draft.Entry{
		Kind:       draft.KindDelete,
		Resource:   draft.Resource(resource),
		Namespace:  namespace,
		Name:       name,
		SourceFile: path,
	})
}

// Unstage removes one pending change (of the given resource - empty means VM)
// from (id, proj)'s draft. An atomic resource unstages as a whole set: its
// entries are one logical change, so removing a single file from under it
// would leave a proposable half-change.
func (c *Coordinator) Unstage(id auth.Identity, proj project.ProjectInfo, resource, namespace, name string) error {
	if r := draft.Resource(resource); r.Atomic() {
		_, err := c.store.ReplaceResource(id.Username, proj.Name, r)
		return err
	}
	return c.store.Unstage(id.Username, proj.Name, draft.Resource(resource), namespace, name)
}

// stage writes entries into (id, proj)'s draft as one change and renders the
// draft that results. Each passes the gate, which pins every entry writing or
// removing a file to the version it weighed, and a whole-file edit never
// replaces staged field edits: they would vanish from the draft without the
// user knowing.
func (c *Coordinator) stage(id auth.Identity, proj project.ProjectInfo, entries ...draft.Entry) (model.DraftView, error) {
	if err := c.gate(id, proj, entries); err != nil {
		return model.DraftView{}, err
	}
	err := c.store.Apply(id.Username, proj.Name, func(d map[string]draft.Entry) error {
		if err := overFieldEdits(d, entries); err != nil {
			return err
		}
		for _, e := range entries {
			d[e.Key()] = e
		}
		return nil
	})
	if err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// overFieldEdits refuses a whole-file edit of an object the draft holds
// setting changes for: they would vanish from the draft without the user
// knowing.
func overFieldEdits(d map[string]draft.Entry, entries []draft.Entry) error {
	for _, e := range entries {
		if staged, ok := d[e.Key()]; ok && fieldEdit(staged) && e.Kind == draft.KindEdit {
			return fmt.Errorf("%w: %s has staged setting changes; propose or unstage them before replacing its whole manifest",
				model.ErrConflict, objectLabel(e))
		}
	}
	return nil
}

// pinOf is the pin of a file read as content, ok=false when it was absent.
func pinOf(content []byte, ok bool) string {
	if !ok {
		return git.AbsentBlob
	}
	return git.BlobID(content)
}

// Discard clears (id, proj)'s draft.
func (c *Coordinator) Discard(id auth.Identity, proj project.ProjectInfo) error {
	return c.store.Clear(id.Username, proj.Name)
}
