package changeset

import (
	"encoding/json"
	"fmt"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
)

// The tenancy and DRS changes a route would stage, unstaged: every access the
// gate would ask of each. The API's capability signals weigh exactly these,
// so a signal and the gate never disagree on what a change makes the cluster
// do. Namespaces are rendered carrying nothing live: what they carry changes
// no access, only the bytes a dry run weighs.

// ProjectNeeds is creating the project rawSpec describes, staged into
// commitProj.
func (c *Coordinator) ProjectNeeds(commitProj project.ProjectInfo, rawSpec json.RawMessage) ([]Access, error) {
	spec, ns, err := parseProject(commitProj, rawSpec)
	if err != nil {
		return nil, err
	}
	entries, _, err := c.projectEntries(commitProj, spec, ns, nil)
	if err != nil {
		return nil, err
	}
	return c.needs(commitProj, entries)
}

// NamespaceNeeds is joining the namespace rawSpec describes to joinProj,
// staged into commitProj.
func (c *Coordinator) NamespaceNeeds(commitProj, joinProj project.ProjectInfo, rawSpec json.RawMessage) ([]Access, error) {
	var spec netgen.NamespaceSpec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return nil, invalid(err)
	}
	entries, err := c.namespaceEntries(commitProj, joinProj, spec)
	if err != nil {
		return nil, err
	}
	return c.needs(commitProj, entries)
}

// AdoptNeeds is attaching a repo to target, owners named, staged into
// commitProj.
func (c *Coordinator) AdoptNeeds(commitProj, target project.ProjectInfo, owners []string) ([]Access, error) {
	repoURL, err := tenantRepoURL(commitProj.Repo, target.Name)
	if err != nil {
		return nil, err
	}
	lives := make([]*netgen.LiveNamespace, len(target.Namespaces))
	entries, err := c.projectAdoption(commitProj, tenantNamespaces(target, repoURL, lives), owners)
	if err != nil {
		return nil, err
	}
	return c.needs(commitProj, entries)
}

// ReleaseNeeds is releasing target, staged into commitProj, and the
// namespaces the release unlabels on the cluster instead (residue).
func (c *Coordinator) ReleaseNeeds(commitProj, target project.ProjectInfo) (need []Access, residue []string, err error) {
	entries, _, residue, err := c.releaseEntries(commitProj, target)
	if err != nil {
		return nil, nil, err
	}
	need, err = c.needs(commitProj, entries)
	return need, residue, err
}

// EnableDRSNeeds is enabling DRS as rawSpec describes, staged into proj.
func (c *Coordinator) EnableDRSNeeds(proj project.ProjectInfo, rawSpec json.RawMessage) ([]Access, error) {
	entries, err := c.enableDRSEntries(proj, rawSpec)
	if err != nil {
		return nil, err
	}
	return c.needs(proj, entries)
}

// DisableDRSNeeds is disabling DRS, staged into proj.
func (c *Coordinator) DisableDRSNeeds(proj project.ProjectInfo) ([]Access, error) {
	entries, _, err := c.disableDRSEntries(proj)
	if err != nil {
		return nil, err
	}
	return c.needs(proj, entries)
}

// ResyncNeeds is syncing VM namespace/name from proj's base branch: Argo
// applying its declared object, a create the authorizer weighs as the update
// of a running VM, or a delete and a create when its sync options replace it.
func (c *Coordinator) ResyncNeeds(proj project.ProjectInfo, namespace, name string) ([]Access, error) {
	path, content, err := c.Manifest(proj, namespace, name)
	if err != nil {
		return nil, err
	}
	objs, err := argoObjects(path, content)
	if err != nil {
		return nil, fmt.Errorf("%w: %s %v; change it in git", model.ErrConflict, path, err)
	}
	for _, o := range objs {
		if o.kind == model.KindVM && o.ref.Namespace == namespace && o.ref.Name == name {
			return objectAccess(o, false)
		}
	}
	return nil, fmt.Errorf("%w: VM %s/%s is not in git", model.ErrNotFound, namespace, name)
}
