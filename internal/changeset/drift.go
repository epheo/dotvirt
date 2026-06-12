package changeset

import (
	"context"
	"fmt"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// The two reconcile directions for out-of-band cluster changes: Adopt proposes
// running→main (git catches up to the cluster), Resync forces main→running (the
// cluster catches up to git). VMDrift renders the gap between them.

// Adopt stages the VM's live (running-branch) state as an edit into (id, proj)'s
// draft, so out-of-band cluster changes can be proposed INTO main (running→main
// reconcile). It diffs running-vs-base and stages an edit making base match running.
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
		return model.DraftView{}, fmt.Errorf("%w: %s/%s not present on the running branch", model.ErrNotFound, namespace, name)
	}
	if !okD {
		return model.DraftView{}, fmt.Errorf("%w: %s/%s not on %s yet; create it instead", model.ErrConflict, namespace, name, c.baseBranch)
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
