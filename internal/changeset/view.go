package changeset

import (
	"fmt"
	"strings"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
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
	view.Warning = c.pruneWarning(proj, entries)
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
			if e.Manifest != "" {
				// A whole-file replacement (template edit): the manifest IS the change.
				item.Changes = []model.Change{{Field: "Edit template", Action: "change", To: e.Name}}
				item.YAML = e.Manifest
				break
			}
			current, _, err := read.FindVMOnBranch(c.baseBranch, e.Namespace, e.Name)
			if err != nil {
				return model.DraftView{}, err
			}
			item.Changes = manifest.ChangesForEdit(current, *e.Edit)
		case draft.KindCreate:
			if e.Manifest != "" {
				// A verbatim-manifest create: a network (UDN/CUDN), an uplink (NNCP),
				// a saved template, a VM rendered from a template, or a VM adopted
				// from the cluster. The manifest IS the change.
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
				case draft.ResourceTemplate:
					field = "Save as template"
				}
				if e.FromTemplate != "" {
					field = "Deploy from template " + e.FromTemplate
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

// pruneWarning names the objects a merged PR would have ArgoCD delete: live and
// tracked by the project's app but absent from git (Argo's own requiresPruning),
// and not spoken for by this draft. Derived on every render, never stored: it
// survives reopening the view, shrinks as adoption stages the missing objects, and
// clears once the merge lands or Argo prunes. A stored warning would keep
// describing a state that no longer exists, which is how a partial capture used to
// read as complete once the response toast was gone.
func (c *Coordinator) pruneWarning(proj project.ProjectInfo, entries []draft.Entry) string {
	if c.prune == nil {
		return ""
	}
	pending := c.prune.PrunePending(proj.Repo, proj.Namespaces)
	if len(pending) == 0 {
		return ""
	}
	covered := map[model.ObjectRef]bool{}
	for _, e := range entries {
		for _, ref := range entryRefs(e) {
			covered[ref] = true
		}
	}
	var missing []string
	for _, ref := range pending {
		if !covered[ref] {
			missing = append(missing, ref.Kind+" "+ref.Namespace+"/"+ref.Name)
		}
	}
	if len(missing) == 0 {
		return ""
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

// entryRefs is the object identity a draft entry speaks for. A manifest-carrying
// entry declares exactly its documents. A wizard create is a VM. A DELETE also
// counts: pruning that object is the draft's stated intent, not a loss to warn
// about. Kinds with no prunable tenant form (uplinks, DRS file sets, templates)
// map to nothing.
func entryRefs(e draft.Entry) []model.ObjectRef {
	if e.Manifest != "" {
		return git.DeclaredRefs(e.SourceFile, []byte(e.Manifest))
	}
	var kind string
	switch e.Resource {
	case "", draft.ResourceVM:
		kind = "VirtualMachine"
	case draft.ResourceNetwork:
		kind = "UserDefinedNetwork"
	case draft.ResourceEgressFirewall:
		kind = "EgressFirewall"
	case draft.ResourceNetworkPolicy:
		kind = "NetworkPolicy"
	case draft.ResourceRoleBinding:
		kind = "RoleBinding"
	default:
		return nil
	}
	return []model.ObjectRef{{Kind: kind, Namespace: e.Namespace, Name: e.Name}}
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
