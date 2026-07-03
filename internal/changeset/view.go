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
		item := model.DraftItem{Kind: string(e.Kind), Resource: string(e.Resource), Namespace: e.Namespace, Name: e.Name}
		switch e.Kind {
		case draft.KindEdit:
			current, _, err := read.FindVMOnBranch(c.baseBranch, e.Namespace, e.Name)
			if err != nil {
				return model.DraftView{}, err
			}
			item.Changes = manifest.ChangesForEdit(current, *e.Edit)
		case draft.KindCreate:
			if e.Manifest != "" {
				// A verbatim-manifest create: a network (UDN/CUDN), an uplink (NNCP),
				// or a VM adopted from the cluster. The manifest IS the change.
				field, to := "Adopt VM from cluster", e.Namespace+"/"+e.Name
				switch e.Resource {
				case draft.ResourceNetwork:
					field = "Create network"
				case draft.ResourceUplink:
					field = "Create uplink"
				case draft.ResourceNamespace:
					field = "Create namespace"
				case draft.ResourceDRS:
					field = "Configure DRS"
				}
				if e.Namespace == ClusterScopeNS || e.Resource == draft.ResourceNamespace {
					to = e.Name // cluster-scoped, or the namespace itself: no prefix
				}
				item.Changes = []model.Change{{Field: field, Action: "add", To: to}}
				item.YAML = e.Manifest
				break
			}
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
