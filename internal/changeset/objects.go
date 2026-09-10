package changeset

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/epheo/dotvirt/internal/auth"

	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
)

// locate finds the base-branch file declaring (resource, namespace, name) - the
// object identity every draft entry carries - under soleDeclarer's rule, since
// a delete or rewrite acts on the whole file. namespace is ClusterScopeNS for a
// cluster-scoped object.
func (c *Coordinator) locate(read *git.Repo, resource draft.Resource, namespace, name string) (string, error) {
	idx, err := read.DeclaredFilesOnBranch(c.baseBranch)
	if err != nil {
		return "", err
	}
	ns := namespace
	if ns == ClusterScopeNS {
		ns = ""
	}
	for _, kind := range resource.Kinds() {
		ref := model.ObjectRef{Kind: kind, Namespace: ns, Name: name}
		if _, ok := idx.Files[ref]; !ok {
			continue
		}
		return soleDeclarer(idx, []model.ObjectRef{ref})
	}
	return "", fmt.Errorf("%w: %s/%s not on %s", model.ErrNotFound, namespace, name, c.baseBranch)
}

// AdoptObject makes git say what the cluster runs for one object: a create when
// git declares nothing, an edit of the declaring file when the running state
// differs from it (the segment and rule counterpart of a VM's drift adoption).
// Rejected when git already matches, so the draft never carries a no-op.
func (c *Coordinator) AdoptObject(id auth.Identity, proj project.ProjectInfo, o Adoptable) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	idx, err := read.DeclaredFilesOnBranch(c.baseBranch)
	if err != nil {
		return model.DraftView{}, err
	}
	path, err := soleDeclarer(idx, git.DeclaredRefs(o.Path, o.Manifest))
	if err != nil {
		return model.DraftView{}, err
	}
	ns := o.Namespace
	if ns == "" {
		ns = ClusterScopeNS
	}
	entry := draft.Entry{
		Kind:       draft.KindCreate,
		Resource:   adoptResource(o.Kind),
		Namespace:  ns,
		Name:       o.Name,
		SourceFile: o.Path,
		Manifest:   string(o.Manifest),
	}
	if path != "" {
		current, err := read.FileOnBranch(c.baseBranch, path)
		if err != nil {
			return model.DraftView{}, err
		}
		if netgen.SameDocument(current, o.Manifest) {
			return model.DraftView{}, fmt.Errorf("%w: %s/%s already matches git", model.ErrInvalid, ns, o.Name)
		}
		entry.Kind, entry.SourceFile = draft.KindEdit, path
	}
	if err := c.store.Stage(id.Username, proj.Name, entry); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// DeclaredFiles maps every object proj's base branch declares to its file - the
// read plane's "declared in git" signal for objects live-first views list.
func (c *Coordinator) DeclaredFiles(proj project.ProjectInfo) (map[model.ObjectRef]string, error) {
	read, err := c.read(proj)
	if err != nil {
		return nil, err
	}
	idx, err := read.DeclaredFilesOnBranch(c.baseBranch)
	if err != nil {
		return nil, err
	}
	return idx.Files, nil
}

// ObjectSpec reads a declared object back as the form spec its create route
// accepts, so the same form can edit it. A manifest the form cannot represent
// (hand-written settings) is refused, never approximated.
func (c *Coordinator) ObjectSpec(proj project.ProjectInfo, resource, namespace, name string) (model.ObjectSpec, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.ObjectSpec{}, err
	}
	path, err := c.locate(read, draft.Resource(resource), namespace, name)
	if err != nil {
		return model.ObjectSpec{}, err
	}
	content, err := read.FileOnBranch(c.baseBranch, path)
	if err != nil {
		return model.ObjectSpec{}, err
	}
	out := model.ObjectSpec{Resource: resource, Namespace: namespace, Name: name, SourceFile: path, Manifest: string(content)}
	spec, err := netgen.Decode(content)
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	if out.Spec, err = json.Marshal(spec); err != nil {
		return model.ObjectSpec{}, err
	}
	return out, nil
}

// StageUpdateManifest replaces a declared object's file with yaml verbatim -
// the edit for a manifest no form expresses, on the same whole-file rule as
// every other edit. The new content must declare exactly the object it
// replaces: a renamed or extra object is a different change.
func (c *Coordinator) StageUpdateManifest(id auth.Identity, proj project.ProjectInfo, resource, namespace, name, yaml string) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	path, err := c.locate(read, draft.Resource(resource), namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	ns := namespace
	if ns == ClusterScopeNS {
		ns = ""
	}
	refs := git.DeclaredRefs(path, []byte(yaml))
	if len(refs) != 1 || refs[0].Namespace != ns || refs[0].Name != name || !slices.Contains(draft.Resource(resource).Kinds(), refs[0].Kind) {
		return model.DraftView{}, fmt.Errorf("%w: the manifest must declare exactly %s/%s", model.ErrInvalid, namespace, name)
	}
	current, err := read.FileOnBranch(c.baseBranch, path)
	if err != nil {
		return model.DraftView{}, err
	}
	if netgen.SameDocument(current, []byte(yaml)) {
		return model.DraftView{}, fmt.Errorf("%w: %s/%s already matches git", model.ErrInvalid, namespace, name)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindEdit,
		Resource:   draft.Resource(resource),
		Namespace:  namespace,
		Name:       name,
		SourceFile: path,
		Manifest:   yaml,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}
