// Package changeset coordinates dotvirt's draft → propose → PR workflow,
// implementing api.Draft. It stages edits/creates into the draft store, renders
// the draft as a semantic (YAML-free) diff, and proposes the whole draft as one
// branch + commit + Forgejo PR off the base branch.
package changeset

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/epheo/dotvirt/internal/api"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/forge"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/vmgen"
)

// VMLookup finds the parsed VM as it appears on a branch, for computing semantic
// diffs. Implemented by the git provider.
type VMLookup interface {
	FindVM(branch, namespace, name string) (model.VM, bool, error)
}

// Resyncer triggers an ArgoCD sync of the Application managing a VM, for the
// main→running drift reconcile. Implemented by the argo client. May be nil.
type Resyncer interface {
	Resync(ctx context.Context, namespace, name string) (model.ResyncResult, error)
}

// Coordinator implements api.Draft.
type Coordinator struct {
	store      *draft.Store
	repo       *git.WriteRepo
	lookup     VMLookup
	forge      *forge.Client // may be nil → degrade to compare URL
	resyncer   Resyncer      // may be nil → re-sync unavailable
	baseBranch string
	proposed   string // working branch name, e.g. dotvirt/proposed
}

// New builds a Coordinator. forge and resyncer may be nil (PR creation degrades
// to a compare link; re-sync becomes unavailable).
func New(store *draft.Store, repo *git.WriteRepo, lookup VMLookup, fc *forge.Client, rs Resyncer, baseBranch, proposedBranch string) *Coordinator {
	return &Coordinator{store: store, repo: repo, lookup: lookup, forge: fc, resyncer: rs, baseBranch: baseBranch, proposed: proposedBranch}
}

// --- staging ---

// StageEdit records a VM edit in the draft.
func (c *Coordinator) StageEdit(namespace, name string, req api.EditRequest) (model.DraftView, error) {
	edit := editFromRequest(req)
	if edit.Empty() {
		return model.DraftView{}, fmt.Errorf("no fields to edit")
	}
	if err := c.store.Stage(draft.Entry{
		Kind:       draft.KindEdit,
		Namespace:  namespace,
		Name:       name,
		SourceFile: req.SourceFile,
		Edit:       &edit,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get()
}

// StageCreate records a new-VM spec in the draft.
func (c *Coordinator) StageCreate(rawSpec json.RawMessage) (model.DraftView, error) {
	var spec vmgen.Spec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return model.DraftView{}, fmt.Errorf("invalid VM spec: %w", err)
	}
	if spec.Name == "" || spec.Namespace == "" {
		return model.DraftView{}, fmt.Errorf("name and namespace are required")
	}
	if err := c.store.Stage(draft.Entry{
		Kind:      draft.KindCreate,
		Namespace: spec.Namespace,
		Name:      spec.Name,
		Spec:      &spec,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get()
}

// Unstage removes one VM's pending change.
func (c *Coordinator) Unstage(namespace, name string) error {
	return c.store.Unstage(namespace, name)
}

// Discard clears the whole draft.
func (c *Coordinator) Discard() error { return c.store.Clear() }

// --- semantic view ---

// Get renders the draft as semantic diff items against the base branch.
func (c *Coordinator) Get() (model.DraftView, error) {
	entries := c.store.List()
	view := model.DraftView{Base: c.baseBranch, Branch: c.proposed, Count: len(entries), Items: []model.DraftItem{}}
	for _, e := range entries {
		item := model.DraftItem{Kind: string(e.Kind), Namespace: e.Namespace, Name: e.Name}
		switch e.Kind {
		case draft.KindEdit:
			current, _, err := c.lookup.FindVM(c.baseBranch, e.Namespace, e.Name)
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

// Propose builds the working branch from the whole draft, pushes it, and opens
// (or finds) a Forgejo PR. Clears the draft on success.
func (c *Coordinator) Propose(req api.ProposeRequest) (model.ProposeResult, error) {
	entries := c.store.List()
	if len(entries) == 0 {
		return model.ProposeResult{}, fmt.Errorf("draft is empty")
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

	res, err := c.repo.CommitChangeset(c.baseBranch, c.proposed, commitMsg, items)
	if err != nil {
		return model.ProposeResult{}, err
	}

	out := model.ProposeResult{Branch: res.Branch, Pushed: res.Pushed}

	if c.forge == nil {
		// No forge configured: hand back a compare URL if we can't even build one,
		// just report the pushed branch.
		return out, c.store.Clear()
	}

	pr, err := c.forge.CreatePR(title, req.Message, c.proposed, c.baseBranch)
	if err != nil {
		// A PR may already exist for this branch; try to find it.
		if existing, ok, ferr := c.forge.FindOpenPR(c.proposed, c.baseBranch); ferr == nil && ok {
			out.PRURL, out.PRNumber, out.Existing = existing.HTMLURL, existing.Number, true
			return out, c.store.Clear()
		}
		out.CompareURL = c.forge.CompareURL(c.proposed, c.baseBranch)
		return out, fmt.Errorf("branch pushed but PR creation failed: %w", err)
	}
	out.PRURL, out.PRNumber = pr.HTMLURL, pr.Number
	return out, c.store.Clear()
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

// Adopt stages the VM's live (running-branch) state as an edit into the draft,
// so out-of-band cluster changes can be proposed INTO main (running→main
// reconcile). It computes the field diff running-vs-main and stages an edit that
// makes main match running.
func (c *Coordinator) Adopt(namespace, name string) (model.DraftView, error) {
	desired, okD, err := c.lookup.FindVM(c.baseBranch, namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	actual, okA, err := c.lookup.FindVM("running", namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	if !okA {
		return model.DraftView{}, fmt.Errorf("%s/%s not present on the running branch", namespace, name)
	}
	if !okD {
		return model.DraftView{}, fmt.Errorf("%s/%s not on %s yet; create it instead", namespace, name, c.baseBranch)
	}

	edit := editToMatch(desired, actual)
	if edit.Empty() {
		return model.DraftView{}, fmt.Errorf("no drift to adopt for %s/%s", namespace, name)
	}
	if err := c.store.Stage(draft.Entry{
		Kind:       draft.KindEdit,
		Namespace:  namespace,
		Name:       name,
		SourceFile: actual.SourceFile,
		Edit:       &edit,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get()
}

// Resync triggers an ArgoCD sync of the Application managing the VM, bringing the
// cluster back to git (main→running reconcile). Writes nothing to git.
func (c *Coordinator) Resync(namespace, name string) (model.ResyncResult, error) {
	if c.resyncer == nil {
		return model.ResyncResult{}, fmt.Errorf("re-sync unavailable (ArgoCD not configured)")
	}
	return c.resyncer.Resync(context.Background(), namespace, name)
}

// VMDrift returns the semantic diff between a VM on the running branch (actual)
// and on the base branch (desired).
func (c *Coordinator) VMDrift(namespace, name string) (model.DriftResult, error) {
	desired, okD, err := c.lookup.FindVM(c.baseBranch, namespace, name)
	if err != nil {
		return model.DriftResult{}, err
	}
	actual, okA, err := c.lookup.FindVM("running", namespace, name)
	if err != nil {
		return model.DriftResult{}, err
	}
	result := model.DriftResult{}
	if !okD || !okA {
		// Present on only one side — treat as drift but we can't field-diff cleanly.
		result.Drift = okD != okA
		return result, nil
	}
	result.Changes = manifest.DiffVMs(desired, actual)
	result.Drift = len(result.Changes) > 0
	return result, nil
}
