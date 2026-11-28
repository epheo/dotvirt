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
// of whoever declares its pools: one grant in the platform repo, beside the
// namespace's admin grant it follows, as the egress grant does.

// ErrDesktopsOn answers a namespace whose desktop grant the platform repo
// holds as rendered: nothing to stage.
var ErrDesktopsOn = fmt.Errorf("%w: desktops are already on", model.ErrInvalid)

// ErrDesktopsUngranted refuses desktops in a namespace the platform repo holds
// no admin-role grant for: the desktop grant goes to its admins and its repo,
// as egress writes do. Granting the project's sync access stages one.
var ErrDesktopsUngranted = fmt.Errorf("%w: desktops follow the namespace's admin grant, which the platform repo does not hold", model.ErrInvalid)

// StageDesktops stages, into the platform draft, what turning desktops on in
// target's namespace ns lacks: the grant of DeskuBe's pool writes and claim
// verbs to the subjects of ns's admin grant and the project's sync account
// (netgen.DesktopsManifest), beside the subjects it already declares.
func (c *Coordinator) StageDesktops(id auth.Identity, commitProj, target project.ProjectInfo, ns string) (model.DraftView, error) {
	_, entries, err := c.desktopsPlan(commitProj, target, ns)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.stage(id, commitProj, entries...)
}

// DesktopsNeeds is StageDesktops's accesses, unstaged, and whether git holds
// ns's desktop grant (on), which stands beside ErrDesktopsOn and
// ErrDesktopsUngranted.
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
	spec, ok, err := grants.adminGrant(ns, target.Name)
	switch {
	case err != nil:
		return on, nil, err
	case !ok:
		return on, nil, fmt.Errorf("%w in %s", ErrDesktopsUngranted, ns)
	}
	entries, err = grants.follow(ns, tenancy.DesktopsBinding(ns), spec, netgen.DesktopsManifest, true)
	switch {
	case err != nil:
		return on, nil, err
	case len(entries) == 0:
		return on, nil, fmt.Errorf("%w in %s", ErrDesktopsOn, ns)
	}
	return on, entries, nil
}
