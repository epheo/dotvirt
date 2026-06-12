package changeset

import (
	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/vmgen"
)

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
		case draft.KindDelete:
			item.Changes = []model.Change{{Field: "lifecycle", Action: "remove", From: e.Namespace + "/" + e.Name}}
		}
		view.Items = append(view.Items, item)
	}
	return view, nil
}
