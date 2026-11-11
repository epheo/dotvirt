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
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/publish"
	"github.com/epheo/dotvirt/internal/vmtemplate"
	"github.com/epheo/dotvirt/pkg/forge"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// Resyncer triggers an ArgoCD sync of the Application managing a VM, for the
// main->running drift reconcile. Implemented by the argo client. May be nil.
type Resyncer interface {
	Resync(ctx context.Context, username, namespace, name string) (model.ResyncResult, error)
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

// InstancetypeReader reads one instancetype object under id's own token: the
// one a VM edit switches to, as the VM names it (kind, empty for the
// cluster-scoped default; namespaced kinds live in the VM's namespace). The
// API implements it.
type InstancetypeReader interface {
	Instancetype(id auth.Identity, namespace, kind, name string) (map[string]any, error)
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
	authz    Authorizer          // nil refuses every write (see UseAuthorizer)
	// instancetypes may be nil: an instancetype switch then strips every
	// sizing field an instancetype may take over, and the view says so.
	instancetypes InstancetypeReader

	proposing keyedMutex // one Propose per (user, project)
	// syncNS holds the per-project sync accounts the project grants name; ""
	// stages owner grants only.
	syncNS string
	// tenantKinds is what a tenant repo may declare (WithTenantKinds).
	tenantKinds tenancy.Policy
	// usersOpenPRs: dotvirt writes no pull request (WithUsersOpeningPRs).
	usersOpenPRs bool
	// publishing is the platform's publishing settings (WithPublishing).
	publishing publish.Settings
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

// WithPublishing sets the platform's publishing settings; the zero value
// leaves publishing off. Set once, before serving.
func (c *Coordinator) WithPublishing(s publish.Settings) *Coordinator {
	c.publishing = s
	return c
}

// WithSyncNamespace names the ArgoCD namespace holding the per-project sync
// accounts (tenancy.SyncAccount) every project grant includes. Set once,
// before serving.
func (c *Coordinator) WithSyncNamespace(ns string) *Coordinator {
	c.syncNS = ns
	return c
}

// WithUsersOpeningPRs makes dotvirt push branches only: it never opens,
// reopens or edits a pull request, and answers with the compare URL the user
// opens one from. A PR they open on the pushed head is adopted (SettlePushed,
// and any later propose). Set once, before serving.
func (c *Coordinator) WithUsersOpeningPRs(on bool) *Coordinator {
	c.usersOpenPRs = on
	return c
}

// UseInstancetypes attaches the read an instancetype switch makes of its
// target. Set after construction, beside UseAuthorizer.
func (c *Coordinator) UseInstancetypes(r InstancetypeReader) { c.instancetypes = r }

// read is the project repo's mirror, for parsing what the base branch holds.
func (r *Reader) read(proj project.ProjectInfo) (*git.Repo, error) {
	read, _, err := r.pair(proj)
	return read, err
}

// pair is the project's mirror and push clone from ONE Get: a second Get may
// find the entry evicted and the re-clone failing. It hands the caller only the
// failure kind: the raw error can embed the repo URL (credentials included on
// some transports), so it is logged here and never returned.
func (r *Reader) pair(proj project.ProjectInfo) (*git.Repo, *git.WriteRepo, error) {
	if err := requireRepo(proj); err != nil {
		return nil, nil, err
	}
	read, write, err := r.repos.Get(proj.Repo, proj.Path)
	if err != nil {
		log.Printf("changeset: project %s repo: %v", proj.Name, err)
		return nil, nil, fmt.Errorf("%w: project repo unreachable", model.ErrUnavailable)
	}
	return read, write, nil
}

// forgeFailed turns a failed forge call about what into one fixed line
// naming the failure's class. The forge answers dotvirt's own token: its API
// path, host and body are logged here and never returned, and its 401 or 403
// is dotvirt's configuration, not the caller's authority.
func forgeFailed(what string, err error) error {
	log.Printf("changeset: forge: %s: %v", what, err)
	var se *forge.StatusError
	switch {
	case !errors.As(err, &se):
		return fmt.Errorf("%w: %s: the forge is unreachable; try again in a moment", model.ErrUnavailable, what)
	case se.Status == http.StatusNotFound:
		return fmt.Errorf("%w: the forge has no %s", model.ErrNotFound, what)
	case se.Status == http.StatusUnauthorized, se.Status == http.StatusForbidden:
		return fmt.Errorf("%w: %s: the forge refused the console (%d); an administrator must check the console's forge token", model.ErrUnavailable, what, se.Status)
	case se.Status >= http.StatusInternalServerError, se.Status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s: the forge failed (%d); try again in a moment", model.ErrUnavailable, what, se.Status)
	}
	// Any other refusal is dotvirt asking wrong: an internal error.
	return fmt.Errorf("%s: the forge rejected the console's request (%d)", what, se.Status)
}

// gitFailed turns a failed read of commit hash from the project's mirror into
// one fixed line: a commit the mirror lacks is not found, named as the caller
// gave it. Any other failure is inside the mirror, so it is logged here and
// answered as the repo being unreadable.
func gitFailed(hash string, err error) error {
	if errors.Is(err, git.ErrNoCommit) {
		return fmt.Errorf("%w: no commit %s in the project repo", model.ErrNotFound, shortCommit(hash))
	}
	log.Printf("changeset: git: commit %s: %v", hash, err)
	return fmt.Errorf("%w: commit %s: the project repo could not be read", model.ErrUnavailable, shortCommit(hash))
}

func isNotFound(err error) bool {
	var se *forge.StatusError
	return errors.As(err, &se) && se.Status == http.StatusNotFound
}

// write is read's write-side sibling: the mirror beside the push clone a
// commit goes through. Only the write half may hand out the push clone.
func (c *Coordinator) write(proj project.ProjectInfo) (*git.Repo, *git.WriteRepo, error) {
	return c.pair(proj)
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
