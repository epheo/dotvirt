// Package changeset coordinates dotvirt's draft → propose → PR workflow. It stages
// edits/creates into per-(user,project) drafts (staging.go), renders a draft as a
// semantic YAML-free diff (view.go), proposes it as one branch + commit + Forgejo
// PR against that project's repo (propose.go, revert.go), and reconciles the two
// directions of drift (drift.go). Identity and project are passed per call:
// reads/writes target the project's repo, drafts are keyed by the user. It
// satisfies api.Draft without importing api — request/result DTOs live in model.
package changeset

import (
	"context"
	"fmt"
	"log"

	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/vmtemplate"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Resyncer triggers an ArgoCD sync of the Application managing a VM, for the
// main→running drift reconcile. Implemented by the argo client. May be nil.
type Resyncer interface {
	Resync(ctx context.Context, namespace, name string) (model.ResyncResult, error)
}

// Coordinator implements api.Draft. It owns no single repo/identity: each method
// receives the caller's Identity and the target ProjectInfo and resolves the
// repo + branches from there.
type Coordinator struct {
	store    *draft.Store
	repos    *git.RepoSet
	forge    *forge.Factory      // may be nil → degrade to compare URL
	resyncer Resyncer            // may be nil → re-sync unavailable
	renderer vmtemplate.Renderer // processes library templates into VM manifests

	baseBranch    string
	proposed      string // working branch name, e.g. dotvirt/proposed
	runningBranch string // dotvirt-owned branch reflecting live state
}

// New builds a Coordinator. forge and resyncer may be nil (PR creation degrades
// to a compare link; re-sync becomes unavailable).
func New(store *draft.Store, repos *git.RepoSet, ff *forge.Factory, rs Resyncer, baseBranch, proposedBranch, runningBranch string) *Coordinator {
	return &Coordinator{
		store: store, repos: repos, forge: ff, resyncer: rs, renderer: vmtemplate.EngineRenderer{},
		baseBranch: baseBranch, proposed: proposedBranch, runningBranch: runningBranch,
	}
}

// read returns the project repo's read mirror, for parsing VMs during previews.
func (c *Coordinator) read(proj project.ProjectInfo) (*git.Repo, error) {
	if err := requireRepo(proj); err != nil {
		return nil, err
	}
	read, _, err := c.repos.Get(proj.Repo)
	if err != nil {
		// The raw error can embed the repo URL (credentials included on some
		// transports); log it, hand the caller only the kind.
		log.Printf("changeset: project %s repo: %v", proj.Name, err)
		return nil, fmt.Errorf("%w: project repo unreachable", model.ErrUnavailable)
	}
	return read, nil
}

// requireRepo rejects an action on a project with no usable repo BEFORE any draft
// is persisted, so a repoless project never accumulates an orphaned, un-proposable
// entry (and the user gets a clear error instead of a later 500).
func requireRepo(proj project.ProjectInfo) error {
	if proj.Repo == "" {
		if proj.Error != "" {
			return fmt.Errorf("%w: project %q is not editable: %s", model.ErrConflict, proj.Name, proj.Error)
		}
		return fmt.Errorf("%w: project %q has no repo configured", model.ErrConflict, proj.Name)
	}
	return nil
}
