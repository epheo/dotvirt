package changeset

import (
	"fmt"
	"slices"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// Turning desktops on in a project namespace grants there what DeskuBe asks
// of whoever declares its pools and desktops, and of whoever acts on its
// claims. Each grant lives in the platform repo, beside the namespace's admin
// grant it follows, as the egress grant does.

// ErrDesktopsOn answers a namespace whose desktop grants the platform repo
// holds as rendered: nothing to stage.
var ErrDesktopsOn = fmt.Errorf("%w: desktops are already on", model.ErrInvalid)

// ErrDesktopsUngranted refuses desktops in a namespace the platform repo holds
// no admin-role grant for: the desktop writes go to its admins and its repo,
// as egress writes do. Granting the project's sync access stages one.
var ErrDesktopsUngranted = fmt.Errorf("%w: desktops follow the namespace's admin grant, which the platform repo does not hold", model.ErrInvalid)

// StageDesktops stages, into the platform draft, what turning desktops on in
// target's namespace ns lacks (grantSource.desktops).
func (c *Coordinator) StageDesktops(id auth.Identity, commitProj, target project.ProjectInfo, ns string) (model.DraftView, error) {
	_, entries, err := c.desktopsPlan(commitProj, target, ns)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.stage(id, commitProj, entries...)
}

// DesktopsNeeds is StageDesktops's accesses, unstaged, and whether git holds
// ns's desktop grant (on), which stands beside ErrDesktopsOn.
func (c *Coordinator) DesktopsNeeds(commitProj, target project.ProjectInfo, ns string) (on bool, need []Access, err error) {
	on, entries, err := c.desktopsPlan(commitProj, target, ns)
	if err != nil {
		return on, nil, err
	}
	need, err = c.needs(commitProj, entries)
	return on, need, err
}

func (c *Coordinator) desktopsPlan(commitProj, target project.ProjectInfo, ns string) (on bool, entries []draft.Entry, err error) {
	if err := tenantName(target.Name); err != nil {
		return false, nil, err
	}
	if !slices.Contains(target.Namespaces, ns) {
		return false, nil, fmt.Errorf("%w: namespace %s of project %s", model.ErrNotFound, ns, target.Name)
	}
	grants, err := c.grantSource(commitProj)
	if err != nil {
		return false, nil, err
	}
	path, _, err := grants.declared(model.ObjectRef{Kind: model.KindRB.Kind, Namespace: ns, Name: tenancy.DesktopsBinding(ns)}, false)
	if err != nil {
		return false, nil, err
	}
	on = path != ""
	entries, err = grants.desktops(ns, target.Name)
	switch {
	case err != nil:
		return on, nil, err
	case len(entries) == 0:
		return on, nil, fmt.Errorf("%w in %s", ErrDesktopsOn, ns)
	}
	return on, entries, nil
}

// desktops stages ns's desktop grants, each beside the subjects it already
// declares: DeskuBe's writes to the subjects of ns's admin grant and proj's
// sync account (netgen.DesktopsManifest), the acts on anyone's claim to those
// people (desktopsAdmin), and the read of its desktops to its authors
// (netgen.DesktopsViewManifest).
func (g grantSource) desktops(ns, proj string) ([]draft.Entry, error) {
	spec, ok, err := g.adminGrant(ns, proj)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w in %s", ErrDesktopsUngranted, ns)
	}
	writes, err := g.follow(ns, tenancy.DesktopsBinding(ns), spec, netgen.DesktopsManifest, true)
	if err != nil {
		return nil, err
	}
	claims, err := g.desktopsAdmin(spec, true)
	if err != nil {
		return nil, err
	}
	authors, err := g.authors(ns)
	if err != nil || len(authors) == 0 {
		return slices.Concat(writes, claims), err
	}
	view, err := g.follow(ns, tenancy.DesktopsViewBinding(ns), netgen.RoleBindingSpec{Namespace: ns, Project: proj, Kept: authors}, netgen.DesktopsViewManifest, true)
	if err != nil {
		return nil, err
	}
	return slices.Concat(writes, claims, view), nil
}

// desktopsAdmin stages the acts on anyone's claim in spec's namespace for its
// kept subjects and owners, sync accounts aside: a repo holds no claims.
// create=false stages nothing where git declares none.
func (g grantSource) desktopsAdmin(spec netgen.RoleBindingSpec, create bool) ([]draft.Entry, error) {
	spec.SyncAccount = nil
	spec.Kept = slices.DeleteFunc(slices.Clone(spec.Kept), g.syncAccount)
	if len(spec.Kept) == 0 && len(spec.Owners) == 0 {
		return nil, nil
	}
	return g.follow(spec.Namespace, tenancy.DesktopsAdminBinding(spec.Namespace), spec, netgen.DesktopsAdminManifest, create)
}

// authors are the subjects of ns's edit-role grants, sync accounts aside: the
// project's authors, who open consoles and so may read the desktops' pending
// pairing PINs. Its viewers may not.
func (g grantSource) authors(ns string) ([]netgen.Subject, error) {
	if g.bindings == nil {
		return nil, nil
	}
	var out []netgen.Subject
	for _, name := range g.bindings(ns) {
		_, content, err := g.declared(model.ObjectRef{Kind: model.KindRB.Kind, Namespace: ns, Name: name}, false)
		if err != nil {
			return nil, err
		}
		// A grant of a namespaced Role, or one that does not read, names no
		// author.
		role, subjects, ok, err := netgen.DeclaredGrant(content, name)
		if err != nil || !ok || role != "edit" {
			continue
		}
		for _, sub := range subjects {
			if !g.syncAccount(sub) && !slices.Contains(out, sub) {
				out = append(out, sub)
			}
		}
	}
	return out, nil
}
