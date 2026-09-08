package changeset

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Past changes: the base branch's history, one commit reviewed as the semantic
// items a staged draft renders, and what a revert of it would touch. Every
// dotvirt change reaches main as a forge merge, so a merge is the unit reviewed
// and reverted here.

// History lists recent commits on the project's base branch, newest first. A
// forge merge is named by the pull request it merged, so the row reads as what
// the user proposed rather than the forge's merge boilerplate. A repoless
// project has no history, not an error.
func (c *Coordinator) History(proj project.ProjectInfo, limit int) ([]model.Commit, error) {
	if proj.Repo == "" {
		return []model.Commit{}, nil
	}
	read, err := c.read(proj)
	if err != nil {
		return nil, err
	}
	commits, err := read.History(c.baseBranch, limit)
	if err != nil {
		return nil, err
	}
	for i := range commits {
		c.nameByPR(&commits[i], proj)
	}
	return commits, nil
}

// VMHistory lists the base-branch commits that changed a VM's manifest file:
// what changed on this VM and when, from the VM page. A VM not in git has no
// history, not an error - the page already says it is untracked.
func (c *Coordinator) VMHistory(proj project.ProjectInfo, namespace, name string, limit int) ([]model.Commit, error) {
	if proj.Repo == "" {
		return []model.Commit{}, nil
	}
	read, err := c.read(proj)
	if err != nil {
		return nil, err
	}
	vm, found, err := read.FindVMOnBranch(c.baseBranch, namespace, name)
	if err != nil {
		return nil, err
	}
	if !found {
		return []model.Commit{}, nil
	}
	commits, err := read.FileHistory(c.baseBranch, vm.SourceFile, limit)
	if err != nil {
		return nil, err
	}
	for i := range commits {
		c.nameByPR(&commits[i], proj)
	}
	return commits, nil
}

// Commit renders what one past commit did, so it reviews exactly like a pending
// change, plus what reverting it now would do to the base branch.
func (c *Coordinator) Commit(proj project.ProjectInfo, hash string) (model.CommitDetail, error) {
	if err := requireRepo(proj); err != nil {
		return model.CommitDetail{}, err
	}
	read, err := c.read(proj)
	if err != nil {
		return model.CommitDetail{}, err
	}
	d, err := read.CommitDiff(hash)
	if err != nil {
		return model.CommitDetail{}, fmt.Errorf("%w: %v", model.ErrNotFound, err)
	}
	c.nameByPR(&d.Commit, proj)
	out := model.CommitDetail{Commit: d.Commit, Items: commitItems(d.Files)}
	out.RevertWarning, out.Reverted = c.revertState(read, d.Files)
	return out, nil
}

// Proposal renders what merging one open PR would change: the diff from the
// PR's fork point to its head, as the same semantic items a staged draft and a
// past commit render. The head is read from the local mirror, so a branch
// pushed moments ago may not be there until the next fetch - that reads as not
// found, with the retry spelled out.
func (c *Coordinator) Proposal(proj project.ProjectInfo, number int) (model.ProposalDetail, error) {
	if err := requireRepo(proj); err != nil {
		return model.ProposalDetail{}, err
	}
	fc := c.forge.For(proj.Repo)
	if fc == nil {
		return model.ProposalDetail{}, fmt.Errorf("%w: no forge serves %s", model.ErrInvalid, proj.Name)
	}
	pr, err := fc.PR(number)
	if err != nil {
		return model.ProposalDetail{}, fmt.Errorf("%w: pull request #%d: %v", model.ErrNotFound, number, err)
	}
	read, err := c.read(proj)
	if err != nil {
		return model.ProposalDetail{}, err
	}
	files, err := read.BranchDiff(c.baseBranch, pr.Head.Ref)
	if errors.Is(err, git.ErrNoBranch) {
		return model.ProposalDetail{}, fmt.Errorf("%w: branch %s is not mirrored yet; retry in a moment", model.ErrNotFound, pr.Head.Ref)
	}
	if err != nil {
		return model.ProposalDetail{}, err
	}
	return model.ProposalDetail{Proposal: c.proposalRow(proj, pr), Items: commitItems(files)}, nil
}

// nameByPR replaces a forge merge subject with the merged PR's title and link.
// The link is built only for a repo this forge serves: For discards the host,
// so a repo hosted elsewhere would get a link into the wrong forge.
func (c *Coordinator) nameByPR(commit *model.Commit, proj project.ProjectInfo) {
	title, n, ok := forge.MergeSubject(commit.Message)
	if !ok {
		return
	}
	commit.Title, commit.PRNumber = title, n
	if c.forge.SameForge(proj.Repo) {
		if fc := c.forge.For(proj.Repo); fc != nil {
			commit.PRURL = fc.PullURL(n)
		}
	}
}

// revertState: a revert restores each touched file to its pre-commit content
// wholesale, so anything committed to that file since is undone with it. The
// warning names those files. Reverted is true when the base branch already
// carries every file's pre-commit content: a revert would be empty.
func (c *Coordinator) revertState(read *git.Repo, files []git.FileChange) (warning string, reverted bool) {
	var later []string
	reverted = len(files) > 0
	for _, f := range files {
		current, ok, err := read.LookupOnBranch(c.baseBranch, f.Path)
		if err != nil {
			return "", false
		}
		if !ok {
			current = nil
		}
		if !bytes.Equal(current, f.After) {
			later = append(later, f.Path)
		}
		if !bytes.Equal(current, f.Before) {
			reverted = false
		}
	}
	if reverted {
		return "", true
	}
	if len(later) > 0 {
		warning = "Reverting also undoes later changes to " + strings.Join(later, ", ") + "."
	}
	return warning, false
}

// revertFiles is what a revert would do to the base branch as it stands now:
// each restored file against its current content, so the PR body shows the real
// diff of the PR - later changes included - not the commit's mirror image.
func (c *Coordinator) revertFiles(read *git.Repo, items []git.ChangesetItem) []git.FileChange {
	out := make([]git.FileChange, 0, len(items))
	for _, it := range items {
		current, ok, err := read.LookupOnBranch(c.baseBranch, it.Path)
		if err != nil || !ok {
			current = nil
		}
		f := git.FileChange{Path: it.Path, Before: current}
		if !it.Delete {
			f.After = it.NewContent
		}
		out = append(out, f)
	}
	return out
}

// commitItems renders file changes as the draft's semantic items: one per
// declared object. A VM is diffed field by field; every other kind renders as a
// whole-manifest edit, the same shape the draft gives a template or network
// edit. Objects a multi-document file carries unchanged render nothing.
func commitItems(files []git.FileChange) []model.DraftItem {
	items := []model.DraftItem{}
	for _, f := range files {
		before := documentsIn(f.Path, f.Before)
		after := documentsIn(f.Path, f.After)
		for _, doc := range after.order {
			item := model.DraftItem{Namespace: doc.ref.Namespace, Name: doc.ref.Name, Resource: resourceOf(doc.ref.Kind), YAML: doc.raw}
			prev, existed := before.byRef[doc.ref]
			switch {
			case !existed:
				item.Kind = "create"
				item.Changes = createChanges(doc)
			case prev.raw == doc.raw:
				continue
			default:
				item.Kind = "edit"
				item.Changes = editChanges(prev, doc)
				item.BaseYAML = prev.raw
			}
			items = append(items, item)
		}
		for _, doc := range before.order {
			if _, kept := after.byRef[doc.ref]; kept {
				continue
			}
			items = append(items, model.DraftItem{
				Kind: "delete", Namespace: doc.ref.Namespace, Name: doc.ref.Name, Resource: resourceOf(doc.ref.Kind), YAML: doc.raw,
				Changes: []model.Change{{Field: "lifecycle", Action: "remove", From: qualified(doc.ref)}},
			})
		}
	}
	return items
}

// document is one declared object of a manifest file with its own YAML text; vm
// is set when the object parsed as a VirtualMachine.
type document struct {
	ref model.ObjectRef
	raw string
	vm  *model.VM
}

// documentSeparator splits a multi-document file. Splitting the text rather
// than re-encoding keeps each object's YAML as committed for the manifest view.
var documentSeparator = regexp.MustCompile(`(?m)^---[ \t]*$`)

// documentsIn is content's declared objects in order, keyed by identity. An
// undeclared chunk (comments, an empty document) contributes nothing.
func documentsIn(path string, content []byte) orderedDocs {
	out := orderedDocs{byRef: map[model.ObjectRef]document{}}
	if len(content) == 0 {
		return out
	}
	ns := git.DefaultNamespace(path)
	for _, chunk := range documentSeparator.Split(string(content), -1) {
		refs := git.DeclaredRefs(path, []byte(chunk))
		if len(refs) != 1 {
			continue
		}
		doc := document{ref: refs[0], raw: strings.TrimSpace(chunk) + "\n"}
		if doc.ref.Kind == "VirtualMachine" {
			if vms, err := manifest.ParseVMs(path, []byte(chunk), ns); err == nil && len(vms) == 1 {
				doc.vm = &vms[0]
			}
		}
		out.add(doc)
	}
	return out
}

// orderedDocs keeps file order for rendering and identity lookup for pairing.
type orderedDocs struct {
	order []document
	byRef map[model.ObjectRef]document
}

func (o *orderedDocs) add(d document) {
	if _, dup := o.byRef[d.ref]; dup {
		return
	}
	o.order = append(o.order, d)
	o.byRef[d.ref] = d
}

// editChanges is the field diff between two versions of one object. A VM whose
// summarized fields all match still changed (cloud-init, a device detail), so
// it renders as a whole-manifest edit rather than nothing.
func editChanges(prev, doc document) []model.Change {
	if prev.vm != nil && doc.vm != nil {
		if changes := manifest.DiffVMs(*prev.vm, *doc.vm); len(changes) > 0 {
			return changes
		}
	}
	return []model.Change{{Field: "Edit " + kindLabel(doc.ref.Kind), Action: "change", To: doc.ref.Name}}
}

// createChanges summarizes a new object: a VM by its sizing and devices, any
// other kind by its identity.
func createChanges(doc document) []model.Change {
	nsName := qualified(doc.ref)
	if doc.vm == nil {
		return []model.Change{{Field: "Create " + kindLabel(doc.ref.Kind), Action: "add", To: nsName}}
	}
	vm := doc.vm
	out := []model.Change{{Field: "Create VM", Action: "add", To: nsName}}
	add := func(field, to string) {
		if to != "" {
			out = append(out, model.Change{Field: field, Action: "add", To: to})
		}
	}
	add("Instance type", vm.Instancetype)
	add("Preference", vm.Preference)
	if vm.CPUCores > 0 {
		add("CPU", fmt.Sprintf("%d vCPU", vm.CPUCores))
	}
	add("Memory", vm.Memory)
	for _, d := range vm.Disks {
		if d.Size == "" {
			add("Disk", d.Name)
			continue
		}
		add("Disk", manifest.DiskLabel(d.Name, d.Size, d.StorageClass))
	}
	for _, n := range vm.Networks {
		add("Network", n.Name)
	}
	add("Power", string(vm.Power))
	return out
}

// qualified is ns/name, or the bare name for a cluster-scoped object.
func qualified(ref model.ObjectRef) string {
	if ref.Namespace == "" {
		return ref.Name
	}
	return ref.Namespace + "/" + ref.Name
}

// resourceOf maps a declared kind onto DraftItem.Resource: "" for a VM (the
// draft's default), the kind name for anything else.
func resourceOf(kind string) string {
	if kind == "VirtualMachine" {
		return ""
	}
	return kind
}

func kindLabel(kind string) string {
	if kind == "VirtualMachine" {
		return "VM"
	}
	return kind
}
