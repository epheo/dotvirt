// Package changeset coordinates dotvirt's draft -> propose -> PR workflow. It stages
// edits/creates into per-(user,project) drafts (staging.go), renders a draft as a
// semantic YAML-free diff (view.go), proposes it as one branch + commit + Forgejo
// PR against that project's repo (propose.go, revert.go), and reconciles the two
// directions of drift (drift.go). Identity and project are passed per call:
// reads/writes target the project's repo, drafts are keyed by the user. It
// satisfies api.Reader and api.Draft without importing api - request/result DTOs
// live in model.
package changeset

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/vmtemplate"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Resyncer triggers an ArgoCD sync of the Application managing a VM, for the
// main->running drift reconcile. Implemented by the argo client. May be nil.
type Resyncer interface {
	Resync(ctx context.Context, namespace, name string) (model.ResyncResult, error)
}

// LiveSource supplies the cluster's current VM manifests, serialized exactly as git
// holds them. It is the "actual" side of drift and what adoption captures. Reading the
// in-memory snapshot keeps live state out of git entirely: no write per tick to every
// tenant repo, nothing to lag behind the cluster, and nothing lost with the repo.
type LiveSource interface {
	VMManifests(namespaces []string) []model.File
	// Ready is false while the backing reflector is still on its initial LIST, when a
	// partial answer would read as "these VMs are gone".
	Ready() bool
}

// PruneSource: what ArgoCD would prune for a repo, per Argo's OWN comparison.
// The draft view relays it; re-deriving from git could diverge.
type PruneSource interface {
	PrunePending(repo string, namespaces []string) []model.ObjectRef
}

// Reader serves the read-only views of a project's git and forge state:
// history, proposals, manifests, templates, declared files, drift. Its
// methods need neither the caller's identity nor a draft, which is what lets
// them implement api.Reader on their own and test without a draft store.
// Coordinator embeds it.
type Reader struct {
	repos *git.RepoSet
	forge *forge.Factory // may be nil -> no forge views, PR links or reopen
	live  LiveSource     // may be nil -> adoption and drift unavailable

	baseBranch string
	proposed   string // working branch prefix, e.g. dotvirt/proposed

	reviewMu sync.Mutex
	reviewed map[string]map[int]reviewed // per repo: open PR -> review state at its last head
}

// NewReader builds the read half on its own. ff and live may be nil.
func NewReader(repos *git.RepoSet, ff *forge.Factory, live LiveSource, baseBranch, proposedBranch string) *Reader {
	return &Reader{repos: repos, forge: ff, live: live, baseBranch: baseBranch, proposed: proposedBranch}
}

// Coordinator implements api.Draft over a Reader: the write half, staging into
// per-(user, project) drafts and turning them into branches and pull requests.
// It owns no single repo/identity: each method receives the caller's Identity
// and the target ProjectInfo and resolves the repo + branches from there.
type Coordinator struct {
	*Reader
	store    *draft.Store
	resyncer Resyncer            // may be nil -> re-sync unavailable
	renderer vmtemplate.Renderer // processes library templates into VM manifests
	prune    PruneSource         // may be nil -> the draft view carries no prune warning
}

// New builds a Coordinator. forge and resyncer may be nil (PR creation degrades
// to a compare link; re-sync becomes unavailable).
func New(store *draft.Store, repos *git.RepoSet, ff *forge.Factory, rs Resyncer, live LiveSource, prune PruneSource, baseBranch, proposedBranch string) *Coordinator {
	return &Coordinator{
		Reader:   NewReader(repos, ff, live, baseBranch, proposedBranch),
		store:    store,
		resyncer: rs,
		renderer: vmtemplate.EngineRenderer{},
		prune:    prune,
	}
}

// read is the project repo's mirror, for parsing what the base branch holds.
// It hands the caller only the failure kind: the raw error can embed the repo
// URL (credentials included on some transports), so it is logged here and
// never returned.
func (r *Reader) read(proj project.ProjectInfo) (*git.Repo, error) {
	if err := requireRepo(proj); err != nil {
		return nil, err
	}
	read, _, err := r.repos.Get(proj.Repo)
	if err != nil {
		log.Printf("changeset: project %s repo: %v", proj.Name, err)
		return nil, fmt.Errorf("%w: project repo unreachable", model.ErrUnavailable)
	}
	return read, nil
}

// write is read's write-side sibling: the mirror beside the push clone a
// commit goes through. Only the write half may hand out the push clone; the
// second Get is the pair read just opened.
func (c *Coordinator) write(proj project.ProjectInfo) (*git.Repo, *git.WriteRepo, error) {
	read, err := c.read(proj)
	if err != nil {
		return nil, nil, err
	}
	_, write, _ := c.repos.Get(proj.Repo)
	return read, write, nil
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
