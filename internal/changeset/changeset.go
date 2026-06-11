// Package changeset coordinates dotvirt's draft → propose → PR workflow. It stages
// edits/creates into per-(user,project) drafts, renders a draft as a semantic
// (YAML-free) diff, and proposes it as one branch + commit + Forgejo PR against
// that project's repo. Identity and project are passed per call: reads/writes
// target the project's repo, drafts are keyed by the user. It satisfies api.Draft
// without importing api — request/result DTOs live in model.
package changeset

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/forge"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/vmgen"
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
	forge    *forge.Factory // may be nil → degrade to compare URL
	resyncer Resyncer       // may be nil → re-sync unavailable

	baseBranch    string
	proposed      string // working branch name, e.g. dotvirt/proposed
	runningBranch string // dotvirt-owned branch reflecting live state
}

// New builds a Coordinator. forge and resyncer may be nil (PR creation degrades
// to a compare link; re-sync becomes unavailable).
func New(store *draft.Store, repos *git.RepoSet, ff *forge.Factory, rs Resyncer, baseBranch, proposedBranch, runningBranch string) *Coordinator {
	return &Coordinator{
		store: store, repos: repos, forge: ff, resyncer: rs,
		baseBranch: baseBranch, proposed: proposedBranch, runningBranch: runningBranch,
	}
}

// read returns the project repo's read mirror, for parsing VMs during previews.
func (c *Coordinator) read(proj project.ProjectInfo) (*git.Repo, error) {
	if err := requireRepo(proj); err != nil {
		return nil, err
	}
	read, _, err := c.repos.Get(proj.Repo)
	return read, err
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

// --- staging ---

// StageEdit records a VM edit in (id, proj)'s draft.
func (c *Coordinator) StageEdit(id auth.Identity, proj project.ProjectInfo, namespace, name string, req model.EditRequest) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	edit := editFromRequest(req)
	if edit.Empty() {
		return model.DraftView{}, fmt.Errorf("%w: no fields to edit", model.ErrInvalid)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindEdit,
		Namespace:  namespace,
		Name:       name,
		SourceFile: req.SourceFile,
		Edit:       &edit,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// StageCreate records a new-VM spec in (id, proj)'s draft.
func (c *Coordinator) StageCreate(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	var spec vmgen.Spec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: invalid VM spec: %v", model.ErrInvalid, err)
	}
	if spec.Name == "" || spec.Namespace == "" {
		return model.DraftView{}, fmt.Errorf("%w: name and namespace are required", model.ErrInvalid)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:      draft.KindCreate,
		Namespace: spec.Namespace,
		Name:      spec.Name,
		Spec:      &spec,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// Unstage removes one VM's pending change from (id, proj)'s draft.
func (c *Coordinator) Unstage(id auth.Identity, proj project.ProjectInfo, namespace, name string) error {
	return c.store.Unstage(id.Username, proj.Name, namespace, name)
}

// Discard clears (id, proj)'s draft.
func (c *Coordinator) Discard(id auth.Identity, proj project.ProjectInfo) error {
	return c.store.Clear(id.Username, proj.Name)
}

// --- semantic view ---

// Get renders (id, proj)'s draft as semantic diff items against the base branch.
func (c *Coordinator) Get(id auth.Identity, proj project.ProjectInfo) (model.DraftView, error) {
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		return model.DraftView{}, err
	}
	view := model.DraftView{Base: c.baseBranch, Branch: c.proposedBranch(id.Username, proj.Name), Count: len(entries), Items: []model.DraftItem{}}
	if len(entries) == 0 {
		return view, nil
	}
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	for _, e := range entries {
		item := model.DraftItem{Kind: string(e.Kind), Namespace: e.Namespace, Name: e.Name}
		switch e.Kind {
		case draft.KindEdit:
			current, _, err := read.FindVMOnBranch(c.baseBranch, e.Namespace, e.Name)
			if err != nil {
				return model.DraftView{}, err
			}
			item.Changes = manifest.ChangesForEdit(current, *e.Edit)
		case draft.KindCreate:
			item.Changes = changesForCreate(*e.Spec)
			if _, content, err := vmgen.Manifest(*e.Spec); err == nil {
				item.YAML = string(content)
			}
		}
		view.Items = append(view.Items, item)
	}
	return view, nil
}

// --- propose ---

// Propose builds the working branch from (id, proj)'s whole draft, pushes it to
// the project repo, and opens (or finds) a Forgejo PR. Clears the draft on success.
func (c *Coordinator) Propose(id auth.Identity, proj project.ProjectInfo, req model.ProposeRequest) (model.ProposeResult, error) {
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		return model.ProposeResult{}, err
	}
	if len(entries) == 0 {
		return model.ProposeResult{}, fmt.Errorf("%w: draft is empty", model.ErrInvalid)
	}
	if err := requireRepo(proj); err != nil {
		return model.ProposeResult{}, err
	}
	_, write, err := c.repos.Get(proj.Repo)
	if err != nil {
		return model.ProposeResult{}, err
	}

	items, err := c.toChangesetItems(entries)
	if err != nil {
		return model.ProposeResult{}, err
	}

	title := req.Title
	if title == "" {
		title = fmt.Sprintf("dotvirt: %d change(s)", len(entries))
	}
	commitMsg := title
	if req.Message != "" {
		commitMsg = title + "\n\n" + req.Message
	}

	// The working branch is per-(user, project): a constant branch would be
	// force-pushed by every user proposing into the same repo, so concurrent
	// proposals would clobber each other's PR. Scoping it isolates them.
	branch := c.proposedBranch(id.Username, proj.Name)

	// Attribute the commit to the k8s user (committer stays dotvirt, the SA that
	// pushes). K8s usernames aren't emails, so synthesize a stable noreply address.
	by := git.Author{Name: id.Username, Email: authorEmail(id.Username)}
	res, err := write.CommitChangeset(c.baseBranch, branch, commitMsg, items, by)
	if err != nil {
		return model.ProposeResult{}, err
	}
	out := model.ProposeResult{Branch: res.Branch, Pushed: res.Pushed}

	fc := c.forge.For(proj.Repo)
	if fc == nil {
		// No forge configured (or unparsable repo): report the pushed branch only.
		return out, c.store.Clear(id.Username, proj.Name)
	}

	pr, err := fc.CreatePR(title, req.Message, branch, c.baseBranch)
	if err != nil {
		// A PR may already exist for this branch; try to find it.
		if existing, ok, ferr := fc.FindOpenPR(branch, c.baseBranch); ferr == nil && ok {
			out.PRURL, out.PRNumber, out.Existing = existing.HTMLURL, existing.Number, true
			return out, c.store.Clear(id.Username, proj.Name)
		}
		// Partial success: the branch IS pushed, PR creation just failed (perms,
		// rate-limit, …). Hand back the compare URL so the user can open the PR
		// manually, and KEEP the draft staged so they can retry — this is a 200, not
		// an error (returning err here would make the handler drop the result body).
		log.Printf("propose %s/%s: branch pushed but PR creation failed: %v", proj.Name, branch, err)
		out.CompareURL = fc.CompareURL(branch, c.baseBranch)
		return out, nil
	}
	out.PRURL, out.PRNumber = pr.HTMLURL, pr.Number
	return out, c.store.Clear(id.Username, proj.Name)
}

// proposedBranch derives the per-(user, project) working branch under the
// configured prefix, e.g. dotvirt/proposed/<user>/<project>-<hash>. The readable
// segments are sanitized to valid git refs (refSegment is lossy), so a short hash
// of the RAW (user, project) is appended to guarantee distinct identities never
// share a branch — without it, two usernames that sanitize to the same string
// would force-push over each other's PR.
func (c *Coordinator) proposedBranch(user, project string) string {
	return c.proposed + "/" + refSegment(user) + "/" + refSegment(project) + "-" + shortHash(user, project)
}

func (c *Coordinator) toChangesetItems(entries []draft.Entry) ([]git.ChangesetItem, error) {
	items := make([]git.ChangesetItem, 0, len(entries))
	for _, e := range entries {
		switch e.Kind {
		case draft.KindEdit:
			items = append(items, git.ChangesetItem{
				Path: e.SourceFile, Namespace: e.Namespace, Name: e.Name, Edit: e.Edit,
			})
		case draft.KindCreate:
			path, content, err := vmgen.Manifest(*e.Spec)
			if err != nil {
				return nil, fmt.Errorf("generate %s/%s: %w", e.Namespace, e.Name, err)
			}
			items = append(items, git.ChangesetItem{Path: path, Namespace: e.Namespace, Name: e.Name, NewContent: content})
		}
	}
	return items, nil
}

// --- drift ---

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
