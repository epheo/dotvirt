package changeset

import (
	"fmt"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/vmtemplate"
)

// Read-only git views served over the API. They live here, not in the transport
// layer, so branch names and source-file matching stay behind the coordinator.

// Manifest returns a VM's manifest file as committed on the base branch — the
// raw bytes plus its repo path (the download filename). The git file IS the
// VM's full definition, so this is dotvirt's OVF-export analog.
func (c *Coordinator) Manifest(proj project.ProjectInfo, namespace, name string) (path string, content []byte, err error) {
	read, err := c.read(proj)
	if err != nil {
		return "", nil, err
	}
	vm, found, err := read.FindVMOnBranch(c.baseBranch, namespace, name)
	if err != nil {
		return "", nil, err
	}
	if !found {
		return "", nil, fmt.Errorf("%w: VM %s/%s is not in git", model.ErrNotFound, namespace, name)
	}
	files, err := read.VMManifests(c.baseBranch)
	if err != nil {
		return "", nil, err
	}
	for _, f := range files {
		if f.Path == vm.SourceFile {
			return f.Path, f.Content, nil
		}
	}
	return "", nil, fmt.Errorf("%w: manifest file %s", model.ErrNotFound, vm.SourceFile)
}

// History lists recent commits on the project's base branch — the Changes
// pane's history view. A repoless project has no history, not an error.
func (c *Coordinator) History(proj project.ProjectInfo, limit int) ([]model.Commit, error) {
	if proj.Repo == "" {
		return []model.Commit{}, nil
	}
	read, err := c.read(proj)
	if err != nil {
		return nil, err
	}
	return read.History(c.baseBranch, limit)
}

// Templates lists proj's library as committed on the base branch. An unreadable
// repo degrades to an empty library — the caller's other libraries still list.
func (c *Coordinator) Templates(proj project.ProjectInfo) []model.Template {
	read, err := c.read(proj)
	if err != nil {
		return nil
	}
	files, err := read.TemplatesOnBranch(c.baseBranch)
	if err != nil {
		return nil
	}
	out := make([]model.Template, 0, len(files))
	for _, f := range files {
		out = append(out, vmtemplate.Parse(f.Path, f.Content, proj.Name))
	}
	return out
}
