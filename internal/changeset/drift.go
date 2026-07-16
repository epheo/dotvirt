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
		if err := c.stageAdoptCreate(id.Username, proj.Name, read, actual); err != nil {
			return model.DraftView{}, err
		}
		return c.Get(id, proj)
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
// create). The raw running-branch bytes are staged as-is, at the same path, so
// the proposal is exactly what the exporter saw in the cluster. It only stages
// (the caller renders the view), so AdoptNamespace can loop it before one Get.
func (c *Coordinator) stageAdoptCreate(username, projName string, read *git.Repo, actual model.VM) error {
	content, err := read.FileOnBranch(c.runningBranch, actual.SourceFile)
	if err != nil {
		return err
	}
	return c.store.Stage(username, projName, draft.Entry{
		Kind:       draft.KindCreate,
		Namespace:  actual.Namespace,
		Name:       actual.Name,
		SourceFile: actual.SourceFile,
		Manifest:   string(content),
	})
}

// AdoptNamespace stages every untracked VM in namespace as a create in one draft:
// VMs on the running branch but absent from base — the NotTracked rows the inventory
// shows. A brownfield namespace is thus adopted into git in a single PR instead of
// one drill-down per VM. VMs already in git (tracked, synced or drifted) are left
// alone — drift is adopted per-VM, deliberately. Idempotent: re-staging replaces the
// draft entry; a VM not yet on the running branch (export pending) isn't enumerated,
// so it's skipped rather than failing the batch.
func (c *Coordinator) AdoptNamespace(id auth.Identity, proj project.ProjectInfo, namespace string) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	running, err := read.ParseVMsOnBranch(c.runningBranch)
	if err != nil {
		return model.DraftView{}, err
	}
	base, err := read.ParseVMsOnBranch(c.baseBranch)
	if err != nil {
		return model.DraftView{}, err
	}
	tracked := make(map[string]bool, len(base))
	for _, vm := range base {
		tracked[vm.Namespace+"/"+vm.Name] = true
	}
	staged := 0
	for _, actual := range running {
		if actual.Namespace != namespace || tracked[actual.Namespace+"/"+actual.Name] {
			continue
		}
		if err := c.stageAdoptCreate(id.Username, proj.Name, read, actual); err != nil {
			return model.DraftView{}, err
		}
		staged++
	}
	if staged == 0 {
		return model.DraftView{}, fmt.Errorf("%w: no untracked VMs to adopt in %s", model.ErrInvalid, namespace)
	}
	return c.Get(id, proj)
}

// Resync triggers an ArgoCD sync of the Application managing the VM, bringing the
// cluster back to git (main→running reconcile). Writes nothing to git. It uses the
// SA-identity resyncer (Argo operations have no user context, but the request's
// ctx still bounds the call so a hung Argo op doesn't outlive the HTTP request).
// Because this is the one operation that escalates to dotvirt's SA, the caller's
// own authority over the VM (canUpdateVM, a user-token SSAR) is enforced here,
// beside the escalation — not only at the transport layer.
func (c *Coordinator) Resync(ctx context.Context, canUpdateVM func(context.Context, string, string) (bool, error), namespace, name string) (model.ResyncResult, error) {
	if c.resyncer == nil {
		return model.ResyncResult{}, fmt.Errorf("%w: re-sync unavailable (ArgoCD not configured)", model.ErrUnavailable)
	}
	if allowed, err := canUpdateVM(ctx, namespace, name); err != nil {
		return model.ResyncResult{}, err
	} else if !allowed {
		return model.ResyncResult{}, fmt.Errorf("%w: you don't have permission to sync VM %s/%s", model.ErrForbidden, namespace, name)
	}
	return c.resyncer.Resync(ctx, namespace, name)
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
