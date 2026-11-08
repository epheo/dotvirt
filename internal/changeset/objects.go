package changeset

import (
	"encoding/json"
	"fmt"

	"github.com/epheo/dotvirt/internal/auth"

	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
)

// locate finds the base-branch file declaring (resource, namespace, name) - the
// object identity every draft entry carries - under soleDeclarer's rule, since
// a delete or rewrite acts on the whole file. namespace is model.ClusterScopeNS for a
// cluster-scoped object.
func (r *Reader) locate(read *git.Repo, resource draft.Resource, namespace, name string) (string, error) {
	idx, err := indexDeclared(read, r.baseBranch)
	if err != nil {
		return "", err
	}
	ref, ok := declaredRef(idx, resource, namespace, name)
	if !ok {
		return "", fmt.Errorf("%w: %s/%s not on %s", model.ErrNotFound, namespace, name, r.baseBranch)
	}
	return soleDeclarer(idx, []model.ObjectRef{ref})
}

// declaredRef is the identity under which idx declares (resource, namespace,
// name), whatever file and document it sits in; ok=false when the branch
// declares no such object.
func declaredRef(idx declaredIndex, resource draft.Resource, namespace, name string) (model.ObjectRef, bool) {
	ns := model.ObjectNamespace(namespace)
	for _, kind := range resource.Kinds() {
		ref := model.ObjectRef{Kind: kind, Namespace: ns, Name: name}
		if _, ok := idx.Files[ref]; ok {
			return ref, true
		}
	}
	return model.ObjectRef{}, false
}

// AdoptObject makes git say what the cluster runs for one object: a create when
// git declares nothing, an edit of the declaring file when the running state
// differs from it (the network and rule counterpart of a VM's drift adoption).
// Rejected when git already matches, so the draft never carries a no-op.
func (c *Coordinator) AdoptObject(id auth.Identity, proj project.ProjectInfo, o model.Adoptable) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	idx, err := indexDeclared(read, c.baseBranch)
	if err != nil {
		return model.DraftView{}, err
	}
	path, err := soleDeclarer(idx, declaredRefs(o.Path, o.Manifest))
	if err != nil {
		return model.DraftView{}, err
	}
	ns := model.DraftNamespace(o.Namespace)
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
		if manifest.SameDocument(current, o.Manifest) {
			return model.DraftView{}, fmt.Errorf("%w: %s/%s already matches git", model.ErrInvalid, ns, o.Name)
		}
		entry.Kind, entry.SourceFile, entry.BaseBlob = draft.KindEdit, path, git.BlobID(current)
	}
	return c.stage(id, proj, entry)
}

// DeclaredFiles maps every object proj's base branch declares to its file - the
// read plane's "declared in git" signal for objects live-first views list.
func (r *Reader) DeclaredFiles(proj project.ProjectInfo) (map[model.ObjectRef]string, error) {
	read, err := r.read(proj)
	if err != nil {
		return nil, err
	}
	idx, err := indexDeclared(read, r.baseBranch)
	if err != nil {
		return nil, err
	}
	return idx.Files, nil
}

// ObjectSpec reads a declared object back as the form spec its create route
// accepts, so the same form can edit it. A manifest the form cannot represent
// (hand-written settings) is refused, never approximated.
func (r *Reader) ObjectSpec(proj project.ProjectInfo, resource, namespace, name string) (model.ObjectSpec, error) {
	read, err := r.read(proj)
	if err != nil {
		return model.ObjectSpec{}, err
	}
	path, err := r.locate(read, draft.Resource(resource), namespace, name)
	if err != nil {
		return model.ObjectSpec{}, err
	}
	content, err := read.FileOnBranch(r.baseBranch, path)
	if err != nil {
		return model.ObjectSpec{}, err
	}
	out := model.ObjectSpec{Resource: resource, Namespace: namespace, Name: name, SourceFile: path, Manifest: string(content), BaseBlob: git.BlobID(content)}
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

// StageUpdateManifest replaces a declared object's file with req.YAML
// verbatim - the edit for a manifest no form expresses, on the same
// whole-file rule as every other edit. The gate holds the new content to
// exactly the object it replaces: a renamed or extra object is a different
// change. req.BaseBlob, when set, is the version the editor loaded
// (ObjectSpec): one that is no longer base's is refused, since the edit was
// made on it.
func (c *Coordinator) StageUpdateManifest(id auth.Identity, proj project.ProjectInfo, resource, namespace, name string, req model.UpdateManifestRequest) (model.DraftView, error) {
	yaml := req.YAML
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	path, err := c.locate(read, draft.Resource(resource), namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	current, err := read.FileOnBranch(c.baseBranch, path)
	if err != nil {
		return model.DraftView{}, err
	}
	if err := loadedCurrent(req.BaseBlob, current, namespace+"/"+name, c.baseBranch); err != nil {
		return model.DraftView{}, err
	}
	if manifest.SameDocument(current, []byte(yaml)) {
		return model.DraftView{}, fmt.Errorf("%w: %s/%s already matches git", model.ErrInvalid, namespace, name)
	}
	return c.stage(id, proj, draft.Entry{
		Kind:       draft.KindEdit,
		Resource:   draft.Resource(resource),
		Namespace:  namespace,
		Name:       name,
		SourceFile: path,
		Manifest:   yaml,
		BaseBlob:   git.BlobID(current),
	})
}

// loadedCurrent refuses an edit made on a version of what other than
// current, base's: loaded is the blob the editor read, empty when the
// client did not say.
func loadedCurrent(loaded string, current []byte, what, base string) error {
	if loaded != "" && loaded != git.BlobID(current) {
		return fmt.Errorf("%w: %s changed on %s since the editor loaded it; reload it and make the edit again", model.ErrConflict, what, base)
	}
	return nil
}
