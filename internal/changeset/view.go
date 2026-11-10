package changeset

import (
	"fmt"
	"slices"
	"strings"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Get renders (id, proj)'s draft as semantic diff items against the base branch.
func (c *Coordinator) Get(id auth.Identity, proj project.ProjectInfo) (model.DraftView, error) {
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.render(id, proj, entries)
}

// DraftResult is one project's draft as Drafts renders it, or the error its
// render failed with.
type DraftResult struct {
	Project string
	View    model.DraftView
	Err     error
}

// Drafts renders id's draft of each of projs, in order, that holds an entry
// or carries a warning. Only the drafts the store holds for id read their
// entries and repo; any other renders empty, from the prune source alone.
func (c *Coordinator) Drafts(id auth.Identity, projs []project.ProjectInfo) ([]DraftResult, error) {
	held, err := c.store.Projects(id.Username)
	if err != nil {
		return nil, err
	}
	var out []DraftResult
	for _, p := range projs {
		var entries []draft.Entry
		if slices.Contains(held, p.Name) {
			if entries, err = c.store.List(id.Username, p.Name); err != nil {
				out = append(out, DraftResult{Project: p.Name, Err: err})
				continue
			}
		}
		view, err := c.render(id, p, entries)
		switch {
		case err != nil:
			out = append(out, DraftResult{Project: p.Name, Err: err})
		case view.Count > 0 || view.Warning != "":
			out = append(out, DraftResult{Project: p.Name, View: view})
		}
	}
	return out, nil
}

// pushedWithoutPR is the compare URL of id's branch of proj while dotvirt
// pushed it and no pull request is recorded on it: where the user opens one.
func (c *Coordinator) pushedWithoutPR(id auth.Identity, proj project.ProjectInfo, branch string) string {
	rec, ok, err := c.store.Proposal(id.Username, proj.Name)
	if err != nil || !ok || rec.PR != 0 || rec.Head == "" || rec.Branch != branch || rec.Repo != forge.NormalizeRepoURL(proj.Repo) {
		return ""
	}
	if fc := c.forgeFor(proj.Repo); fc != nil {
		return fc.CompareURL(branch, c.baseBranch)
	}
	return ""
}

// Holds reports whether id's draft of proj holds an entry.
func (c *Coordinator) Holds(id auth.Identity, proj project.ProjectInfo) (bool, error) {
	entries, err := c.store.List(id.Username, proj.Name)
	return len(entries) > 0, err
}

// render is entries as the draft view renders them against the base branch.
func (c *Coordinator) render(id auth.Identity, proj project.ProjectInfo, entries []draft.Entry) (model.DraftView, error) {
	view := model.DraftView{Base: c.baseBranch, Branch: c.proposedBranch(id.Username, proj.Name), Count: len(entries), Items: []model.DraftItem{}}
	view.Prune = c.prunePending(proj, entries)
	view.Warning = pruneWarning(view.Prune)
	if len(entries) == 0 {
		return view, nil
	}
	view.CompareURL = c.pushedWithoutPR(id, proj, view.Branch)
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	for _, e := range entries {
		item := model.DraftItem{Kind: string(e.Kind), Resource: string(e.Resource), Namespace: e.Namespace, Name: e.Name}
		switch e.Kind {
		case draft.KindEdit:
			if e.Manifest != "" {
				// A whole-file replacement: the manifest IS the change. A restore
				// names its version and lists the fields it moves, best effort;
				// a template edit has no field model.
				if e.FromVersion != "" {
					item.Changes = restoreChanges(read, c.baseBranch, e)
				} else {
					item.Changes = []model.Change{{Field: e.Resource.EditLabel(), Action: "change", To: objectLabel(e)}}
				}
				item.YAML = e.Manifest
				if current, ok, err := read.LookupOnBranch(c.baseBranch, e.SourceFile); err == nil && ok {
					item.BaseYAML = string(current)
				}
				break
			}
			current, _, err := findVM(read, c.baseBranch, e.Namespace, e.Name)
			if err != nil {
				return model.DraftView{}, err
			}
			var content []byte
			if b, ok, err := read.LookupOnBranch(c.baseBranch, e.SourceFile); err == nil && ok {
				content = b
			}
			item.Changes = fieldChanges(current, content, e)
			item.Edit = e.Edit
		case draft.KindCreate:
			// The manifest IS the change; the rows only explain it.
			item.YAML = e.Manifest
			switch {
			case e.FromWizard:
				item.Changes = wizardChanges(e)
			case e.FromTemplate != "":
				item.Changes = []model.Change{{Field: "Deploy from template " + e.FromTemplate, Action: "add", To: objectLabel(e)}}
			default:
				item.Changes = []model.Change{{Field: e.Resource.CreateLabel(), Action: "add", To: objectLabel(e)}}
			}
		case draft.KindDelete:
			item.Changes = []model.Change{{Field: "lifecycle", Action: "remove", From: objectLabel(e)}}
		}
		view.Items = append(view.Items, item)
	}
	view.DefaultTitle = defaultTitle(view)
	view.DefaultBody = prBody(view, "", id.Username)
	return view, nil
}

// fieldChanges lists what field edit e changes on current, declared in
// content: a staged edit's rows, and its preview's.
func fieldChanges(current model.VM, content []byte, e draft.Entry) []model.Change {
	out := manifest.ChangesForEdit(current, *e.Edit)
	if content != nil {
		out = append(out, manifest.StripChanges(content, defaultNamespace(e.SourceFile), e.Namespace, e.Name, *e.Edit, targetOf(e))...)
	}
	return out
}

// objectLabel names an entry's object; a namespace's entry is keyed under its
// own name.
func objectLabel(e draft.Entry) string {
	if e.Resource == draft.ResourceNamespace {
		return e.Name
	}
	return qualified(e.Namespace, e.Name)
}

// wizardChanges summarizes a wizard-rendered VM by its sizing and devices, read
// from the staged manifest so the preview cannot drift from what propose
// commits; they are the rows the commit review renders once it is merged.
func wizardChanges(e draft.Entry) []model.Change {
	docs := documentsIn(e.SourceFile, []byte(e.Manifest))
	if len(docs.order) != 1 {
		return []model.Change{{Field: "Create VM", Action: "add", To: objectLabel(e)}}
	}
	return createChanges(docs.order[0])
}

// prunePending: what a merge lets ArgoCD delete, from Argo's own requiresPruning
// minus what the draft speaks for. Derived every render, never stored: a stored
// warning outlived the state and read partial captures as complete.
func (c *Coordinator) prunePending(proj project.ProjectInfo, entries []draft.Entry) []model.ObjectRef {
	if c.prune == nil {
		return nil
	}
	pending := c.prune.PrunePending(proj.Repo, proj.Namespaces)
	if len(pending) == 0 {
		return nil
	}
	covered := map[model.ObjectRef]bool{}
	for _, e := range entries {
		for _, ref := range entryRefs(e) {
			covered[ref] = true
		}
	}
	var missing []model.ObjectRef
	for _, ref := range pending {
		if !covered[ref] {
			missing = append(missing, ref)
		}
	}
	return missing
}

// pruneWarning words the pending prune for the PR body and the views that show
// one sentence; the review lists every object from DraftView.Prune.
func pruneWarning(refs []model.ObjectRef) string {
	if len(refs) == 0 {
		return ""
	}
	missing := make([]string, len(refs))
	for i, ref := range refs {
		missing[i] = ref.Kind + " " + ref.Namespace + "/" + ref.Name
	}
	const maxShown = 4
	more := ""
	if len(missing) > maxShown {
		more = fmt.Sprintf(" (+%d more)", len(missing)-maxShown)
		missing = missing[:maxShown]
	}
	return fmt.Sprintf("Running but not in git: %s%s. ArgoCD deletes them on the next merged change unless they are adopted first.",
		strings.Join(missing, ", "), more)
}

// entryRefs: what a draft entry speaks for. Manifest entries declare their
// documents. A delete counts: that prune is intended. Only VM entries exist
// without a manifest (network and policy changes always carry one), so anything
// else maps to nothing.
func entryRefs(e draft.Entry) []model.ObjectRef {
	if e.Manifest != "" {
		return declaredRefs(e.SourceFile, []byte(e.Manifest))
	}
	if e.Resource == "" || e.Resource == draft.ResourceVM {
		return []model.ObjectRef{{Kind: "VirtualMachine", Namespace: e.Namespace, Name: e.Name}}
	}
	return nil
}

// JoinWarning folds non-fatal notes into DraftView's single Warning string.
func JoinWarning(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + " " + b
}

// restoreChanges renders a version restore as the field moves it makes, under
// a row naming the version. The verbatim manifest is what gets committed; the
// field list only explains it, so an unparseable side degrades to the name.
func restoreChanges(read *git.Repo, base string, e draft.Entry) []model.Change {
	out := []model.Change{{Field: "Restore", Action: "change", To: "version " + e.FromVersion}}
	current, found, err := findVM(read, base, e.Namespace, e.Name)
	if err != nil || !found {
		return out
	}
	vms, err := manifest.ParseVMs(e.SourceFile, []byte(e.Manifest), e.Namespace)
	if err != nil {
		return out
	}
	for _, vm := range vms {
		if vm.Namespace == e.Namespace && vm.Name == e.Name {
			return append(out, manifest.ChangesForEdit(current, manifest.EditToMatch(current, vm))...)
		}
	}
	return out
}
