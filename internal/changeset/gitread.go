package changeset

import (
	"fmt"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/vmtemplate"
)

// Read-only git views served over the API. They live here, not in the transport
// layer, so branch names and source-file matching stay behind the coordinator.

// Manifest returns a VM's manifest file as committed on the base branch - the
// raw bytes plus its repo path (the download filename). The git file IS the
// VM's full definition, so this is dotvirt's VM-export path.
func (r *Reader) Manifest(proj project.ProjectInfo, namespace, name string) (path string, content []byte, err error) {
	read, err := r.read(proj)
	if err != nil {
		return "", nil, err
	}
	vm, found, err := findVM(read, r.baseBranch, namespace, name)
	if err != nil {
		return "", nil, err
	}
	if !found {
		return "", nil, fmt.Errorf("%w: VM %s/%s is not in git", model.ErrNotFound, namespace, name)
	}
	content, err = read.FileOnBranch(r.baseBranch, vm.SourceFile)
	if err != nil {
		return "", nil, err
	}
	return vm.SourceFile, content, nil
}

// Templates lists proj's library as committed on the base branch. An unreadable
// repo degrades to an empty library - the caller's other libraries still list.
func (r *Reader) Templates(proj project.ProjectInfo) []model.Template {
	read, err := r.read(proj)
	if err != nil {
		return nil
	}
	files, err := read.TemplatesOnBranch(r.baseBranch)
	if err != nil {
		return nil
	}
	out := make([]model.Template, 0, len(files))
	for _, f := range files {
		out = append(out, vmtemplate.Parse(f.Path, f.Content, proj.Name))
	}
	return out
}
