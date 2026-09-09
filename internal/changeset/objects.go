package changeset

import (
	"encoding/json"
	"fmt"

	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
)

// locate finds the base-branch file declaring (resource, namespace, name) - the
// object identity every draft entry carries - refusing a file that also declares
// other objects, since a delete or rewrite acts on the whole file. namespace is
// ClusterScopeNS for a cluster-scoped object.
func (c *Coordinator) locate(read *git.Repo, resource draft.Resource, namespace, name string) (string, error) {
	files, err := read.DeclaredFilesOnBranch(c.baseBranch)
	if err != nil {
		return "", err
	}
	ns := namespace
	if ns == ClusterScopeNS {
		ns = ""
	}
	for _, kind := range resource.Kinds() {
		path, ok := files[model.ObjectRef{Kind: kind, Namespace: ns, Name: name}]
		if !ok {
			continue
		}
		for ref, p := range files {
			if p == path && (ref.Kind != kind || ref.Name != name) {
				return "", fmt.Errorf("%w: %s/%s is declared in %s beside other objects; change it in git", model.ErrConflict, namespace, name, path)
			}
		}
		return path, nil
	}
	return "", fmt.Errorf("%w: %s/%s not on %s", model.ErrNotFound, namespace, name, c.baseBranch)
}

// DeclaredFiles maps every object proj's base branch declares to its file - the
// read plane's "declared in git" signal for objects live-first views list.
func (c *Coordinator) DeclaredFiles(proj project.ProjectInfo) (map[model.ObjectRef]string, error) {
	read, err := c.read(proj)
	if err != nil {
		return nil, err
	}
	return read.DeclaredFilesOnBranch(c.baseBranch)
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
	spec, err := netgen.Decode(content)
	if err != nil {
		return model.ObjectSpec{}, fmt.Errorf("%w: %s: %v", model.ErrConflict, path, err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return model.ObjectSpec{}, err
	}
	return model.ObjectSpec{Resource: resource, Namespace: namespace, Name: name, SourceFile: path, Spec: raw}, nil
}
