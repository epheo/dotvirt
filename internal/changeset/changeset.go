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

// LiveSource supplies the cluster's current VM manifests, serialized exactly as git
// holds them. It is the "actual" side of drift and what adoption captures. Reading the
// in-memory snapshot keeps live state out of git entirely: no write per tick to every
// tenant repo, nothing to lag behind the cluster, and nothing lost with the repo.
type LiveSource interface {
	VMManifests(namespaces []string) []LiveManifest
	// Ready is false while the backing reflector is still on its initial LIST, when a
	// partial answer would read as "these VMs are gone".
	Ready() bool
}

// LiveManifest is one running VM as the repo path and bytes it would occupy.
type LiveManifest struct {
	Path    string
	Content []byte
}

// PruneSource reports what ArgoCD would prune for a project's repo: objects live and
// tracked by its Application but absent from git, per Argo's OWN last comparison. That
// comparison is the single authority on what a merge-triggered sync deletes, so the
// draft view relays it rather than re-deriving from git and risking divergence.
type PruneSource interface {
	PrunePending(repo string, namespaces []string) []model.ObjectRef
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

	live  LiveSource  // may be nil -> adoption and drift unavailable
	prune PruneSource // may be nil -> the draft view carries no prune warning

	baseBranch string
	proposed   string // working branch name, e.g. dotvirt/proposed
}

// New builds a Coordinator. forge and resyncer may be nil (PR creation degrades
// to a compare link; re-sync becomes unavailable).
func New(store *draft.Store, repos *git.RepoSet, ff *forge.Factory, rs Resyncer, live LiveSource, prune PruneSource, baseBranch, proposedBranch string) *Coordinator {
	return &Coordinator{
		store: store, repos: repos, forge: ff, resyncer: rs, live: live, prune: prune, renderer: vmtemplate.EngineRenderer{},
		baseBranch: baseBranch, proposed: proposedBranch,
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
