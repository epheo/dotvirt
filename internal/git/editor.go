package git

import (
	"encoding/json"
	"fmt"

	"github.com/epheo/dotvirt/internal/api"
	"github.com/epheo/dotvirt/internal/vmgen"
)

// Editor adapts WriteRepo to the api.Editor interface: it turns an edit request
// into a feature-branch commit. The branch name is derived from the VM and a
// caller-supplied counter so repeated edits don't collide.
type Editor struct {
	repo      *WriteRepo
	branchSeq func() int // supplies a monotonic suffix for feature branch names
}

// NewEditor wraps a WriteRepo as an api.Editor. seq returns an increasing number
// used to make feature branch names unique per edit.
func NewEditor(repo *WriteRepo, seq func() int) *Editor {
	return &Editor{repo: repo, branchSeq: seq}
}

// EditVM applies the requested field changes and commits them to a new feature
// branch off the source branch, returning the EditResult (branch, diff, hash).
func (e *Editor) EditVM(namespace, name string, req api.EditRequest) (any, error) {
	edit := VMEdit{
		Power:          req.Power,
		CPUCores:       req.CPUCores,
		Memory:         req.Memory,
		Instancetype:   req.Instancetype,
		Preference:     req.Preference,
		SetLabels:      req.SetLabels,
		RemoveLabels:   req.RemoveLabels,
		RemoveDisks:    req.RemoveDisks,
		RemoveNetworks: req.RemoveNetworks,
	}
	for _, d := range req.AddDisks {
		edit.AddDisks = append(edit.AddDisks, DiskAdd{Name: d.Name, Size: d.Size})
	}
	for _, n := range req.AddNetworks {
		edit.AddNetworks = append(edit.AddNetworks, NetworkAdd{Name: n.Name})
	}
	if edit.empty() {
		return nil, fmt.Errorf("no fields to edit")
	}

	branch := fmt.Sprintf("dotvirt/edit-%s-%s-%d", namespace, name, e.branchSeq())
	message := req.Message
	if message == "" {
		message = commitMessage(namespace, name, edit)
	}

	return e.repo.CommitVMEdit(req.SourceBranch, branch, req.SourceFile, namespace, name, message, edit)
}

// CreateVM generates a VirtualMachine manifest from the wizard spec (raw JSON)
// and commits it to a new feature branch off sourceBranch. Returns the
// EditResult (branch, diff, hash).
func (e *Editor) CreateVM(sourceBranch string, rawSpec json.RawMessage) (any, error) {
	var spec vmgen.Spec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return nil, fmt.Errorf("invalid VM spec: %w", err)
	}
	path, content, err := vmgen.Manifest(spec)
	if err != nil {
		return nil, err
	}
	branch := fmt.Sprintf("dotvirt/create-%s-%s-%d", spec.Namespace, spec.Name, e.branchSeq())
	message := fmt.Sprintf("dotvirt: create %s/%s", spec.Namespace, spec.Name)
	return e.repo.CommitNewFile(sourceBranch, branch, path, message, content)
}

func commitMessage(namespace, name string, edit VMEdit) string {
	changes := ""
	add := func(s string) {
		if changes != "" {
			changes += ", "
		}
		changes += s
	}
	if edit.Power != nil {
		add("power=" + *edit.Power)
	}
	if edit.CPUCores != nil {
		add(fmt.Sprintf("cpu=%d", *edit.CPUCores))
	}
	if edit.Memory != nil {
		add("memory=" + *edit.Memory)
	}
	if edit.Instancetype != nil {
		add("instancetype=" + *edit.Instancetype)
	}
	if edit.Preference != nil {
		add("preference=" + *edit.Preference)
	}
	if len(edit.SetLabels) > 0 || len(edit.RemoveLabels) > 0 {
		add("labels")
	}
	for _, d := range edit.AddDisks {
		add("+disk " + d.Name)
	}
	for _, d := range edit.RemoveDisks {
		add("-disk " + d)
	}
	for _, n := range edit.AddNetworks {
		add("+net " + n.Name)
	}
	for _, n := range edit.RemoveNetworks {
		add("-net " + n)
	}
	return fmt.Sprintf("dotvirt: edit %s/%s (%s)", namespace, name, changes)
}
