package changeset

import (
	"context"
	"fmt"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// The two reconcile directions for out-of-band cluster changes: Adopt proposes
// running→main (git catches up to the cluster), Resync forces main→running (the
// cluster catches up to git). VMDrift renders the gap between them.

// Adopt stages the VM's live (running-branch) state into (id, proj)'s draft, so
// out-of-band cluster changes can be proposed INTO main (running→main
// reconcile): as an edit making base match running when the VM is tracked, or —
// when it exists only in the cluster (e.g. a fresh clone target) — as a create
// of its running-branch manifest.
func (c *Coordinator) Adopt(id auth.Identity, proj project.ProjectInfo, namespace, name string) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	desired, okD, err := read.FindVMOnBranch(c.baseBranch, namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	actual, okA, err := read.FindVMOnBranch(c.runningBranch, namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	if !okA {
		// Also the cluster-only case before the exporter's next tick has written
		// the VM to the running branch — hence "yet".
		return model.DraftView{}, fmt.Errorf("%w: %s/%s not on the running branch yet (live export may be pending)", model.ErrNotFound, namespace, name)
	}
	if !okD {
		return c.stageAdoptCreate(id, proj, read, actual)
	}

	edit := editToMatch(desired, actual)
	if edit.Empty() {
		return model.DraftView{}, fmt.Errorf("%w: no drift to adopt for %s/%s", model.ErrInvalid, namespace, name)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindEdit,
		Namespace:  namespace,
		Name:       name,
		SourceFile: actual.SourceFile,
		Edit:       &edit,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// stageAdoptCreate stages a brand-new manifest from the running branch — the
// adopt path for a VM with no file on base (a clone target, an out-of-band
// create). The raw running-branch bytes are proposed as-is, at the same path,
// so the proposal is exactly what the exporter saw in the cluster.
func (c *Coordinator) stageAdoptCreate(id auth.Identity, proj project.ProjectInfo, read *git.Repo, actual model.VM) (model.DraftView, error) {
	content, err := read.FileOnBranch(c.runningBranch, actual.SourceFile)
	if err != nil {
		return model.DraftView{}, err
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindCreate,
		Namespace:  actual.Namespace,
		Name:       actual.Name,
		SourceFile: actual.SourceFile,
		Manifest:   string(content),
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// Resync triggers an ArgoCD sync of the Application managing the VM, bringing the
// cluster back to git (main→running reconcile). Writes nothing to git. It uses the
// SA-identity resyncer (Argo operations have no user context).
func (c *Coordinator) Resync(namespace, name string) (model.ResyncResult, error) {
	if c.resyncer == nil {
		return model.ResyncResult{}, fmt.Errorf("%w: re-sync unavailable (ArgoCD not configured)", model.ErrUnavailable)
	}
	return c.resyncer.Resync(context.Background(), namespace, name)
}

// VMDrift returns the semantic diff between a VM on the running branch (actual)
// and on the base branch (desired), within proj's repo.
func (c *Coordinator) VMDrift(proj project.ProjectInfo, namespace, name string) (model.DriftResult, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DriftResult{}, err
	}
	desired, okD, err := read.FindVMOnBranch(c.baseBranch, namespace, name)
	if err != nil {
		return model.DriftResult{}, err
	}
	actual, okA, err := read.FindVMOnBranch(c.runningBranch, namespace, name)
	if err != nil {
		return model.DriftResult{}, err
	}
	result := model.DriftResult{}
	if !okD || !okA {
		result.Drift = okD != okA
		return result, nil
	}
	result.Changes = manifest.DiffVMs(desired, actual)
	result.Drift = len(result.Changes) > 0
	return result, nil
}
