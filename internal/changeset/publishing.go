package changeset

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/publish"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// PublishTarget is what the API's snapshots say of a project's namespaces.
type PublishTarget struct {
	// PublishNamespace: the cluster holds the project's publish namespace
	// (tenancy.PublishNamespace), which the platform creates with the project.
	PublishNamespace bool
	// PublishNameHeld: another namespace holds that name; PublishNameRequested:
	// a user's own project, which a self-provisioner may request.
	PublishNameHeld, PublishNameRequested bool
	// Networks is each namespace's primary network, the publish namespace's
	// among them, "default" for the cluster's own: namespaces sharing one
	// reach each other's Services.
	Networks map[string]string
	// RoutesGranted: the cluster's admin role already writes HTTPRoutes, so
	// no namespace needs a route grant of its own.
	RoutesGranted bool
	// Isolated: the publishing GatewayClass says one Gateway can never
	// delay another's configuration, so a Gateway on a VM Network may poll
	// its controller through the apiserver.
	Isolated bool
}

// DefaultNetwork is the cluster's own network in PublishTarget.Networks.
const DefaultNetwork = "default"

// PublishPlan is where a project's Gateway goes and whom it serves.
type PublishPlan struct {
	// Namespace holds the Gateway: the project's publish namespace, where no
	// tenant grant reaches.
	Namespace string
	// Reach is the project's namespaces on that namespace's network. One with
	// a VM Network of its own is not: no gateway joins two primary networks.
	Reach []string
	// On: git holds the project's Gateway.
	On bool
	// Primary: that network is a VM Network, whose pods reach portail's
	// controller only through the apiserver: the Gateway takes parameters
	// and a binding saying so.
	Primary bool
}

// ErrPublishingOn answers a project whose publishing the platform repo holds
// as rendered: nothing to stage.
var ErrPublishingOn = fmt.Errorf("%w: publishing is already on", model.ErrInvalid)

// ErrPrimaryNetwork refuses publishing for a project on a VM Network while
// the publishing GatewayClass does not say its Gateways are isolated.
var ErrPrimaryNetwork = fmt.Errorf("%w: publishing on a VM Network needs a newer portail: today one project could delay the others' route changes", model.ErrInvalid)

// ErrApart refuses publishing, and the publish namespace, for a project none
// of whose namespaces shares the publish namespace's network: each has a VM
// Network of its own, where a gateway is the project's to run.
var ErrApart = fmt.Errorf("%w: no namespace of the project shares its publish namespace's network: each has a VM Network of its own", model.ErrInvalid)

// ErrPublishNameHeld refuses creating a project's publish namespace while
// another namespace holds its name.
var ErrPublishNameHeld = fmt.Errorf("%w: another namespace holds the publish namespace's name: free that name, moving what it holds first, then create the publish namespace", model.ErrConflict)

// ErrPublishNameRequested is ErrPublishNameHeld by a user's own project.
var ErrPublishNameRequested = fmt.Errorf("%w (a user's own project, requested before the platform's: delete it)", ErrPublishNameHeld)

// ErrNoPublishNamespace refuses publishing for a project whose publish
// namespace the cluster lacks. dotvirt never creates it here: a namespace
// joins a VM Network only when created, so only the project's creator knows
// what it must be.
var ErrNoPublishNamespace = fmt.Errorf("%w: publishing needs the project's publish namespace, which the platform creates with the project", model.ErrInvalid)

// StagePublishing stages, into the platform draft, what target's publishing
// lacks: its Gateway, its certificate when the platform names an issuer, the
// HTTPRoute grant of each namespace it reaches where the admin role lacks
// one, and the read of its Gateway for those namespaces' admins.
func (c *Coordinator) StagePublishing(id auth.Identity, commitProj, target project.ProjectInfo, pt PublishTarget) (model.DraftView, error) {
	_, entries, err := c.publishPlan(commitProj, target, pt)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.stage(id, commitProj, entries...)
}

// PublishingNeeds is StagePublishing's plan and accesses, unstaged. The plan
// stands beside ErrPublishingOn; beside ErrPrimaryNetwork and
// ErrNoPublishNamespace the accesses are what it would ask, so a caller who
// could is told what it waits on.
func (c *Coordinator) PublishingNeeds(commitProj, target project.ProjectInfo, pt PublishTarget) (PublishPlan, []Access, error) {
	plan, entries, err := c.publishPlan(commitProj, target, pt)
	if err != nil && !errors.Is(err, ErrPrimaryNetwork) && !errors.Is(err, ErrNoPublishNamespace) {
		return plan, nil, err
	}
	need, nerr := c.needs(commitProj, entries)
	if nerr != nil {
		return plan, nil, nerr
	}
	return plan, need, err
}

// publishPlan is target's plan and each file it stages into commitProj that
// the base branch does not already hold as rendered.
func (c *Coordinator) publishPlan(commitProj, target project.ProjectInfo, pt PublishTarget) (PublishPlan, []draft.Entry, error) {
	var plan PublishPlan
	if !c.publishing.On() {
		return plan, nil, fmt.Errorf("%w: publishing is not configured on this install", model.ErrUnavailable)
	}
	if err := tenantName(target.Name); err != nil {
		return plan, nil, err
	}
	if len(target.Namespaces) == 0 {
		return plan, nil, fmt.Errorf("%w: project %q has no namespace", model.ErrInvalid, target.Name)
	}
	read, err := c.read(commitProj)
	if err != nil {
		return plan, nil, err
	}
	plan.Namespace = tenancy.PublishNamespace(target.Name)
	_, on, err := read.LookupOnBranch(c.baseBranch, model.KindGateway.RepoPath(plan.Namespace, publish.Gateway))
	if err != nil {
		return plan, nil, err
	}
	plan.On = on
	network := func(ns string) string { return cmp.Or(pt.Networks[ns], DefaultNetwork) }
	if pt.PublishNamespace {
		for _, ns := range slices.Sorted(slices.Values(target.Namespaces)) {
			if network(ns) == network(plan.Namespace) {
				plan.Reach = append(plan.Reach, ns)
			}
		}
	}
	plan.Primary = network(plan.Namespace) != DefaultNetwork
	files, err := publish.Manifests(c.publishing, target.Name, plan.Namespace, plan.Primary)
	if err != nil {
		return plan, nil, invalid(err)
	}
	var entries []draft.Entry
	for _, f := range files {
		current, ok, err := read.LookupOnBranch(c.baseBranch, f.Path)
		if err != nil {
			return plan, nil, err
		}
		if ok && bytes.Equal(current, f.Content) {
			continue
		}
		e := draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourcePublishing,
			Namespace:  plan.Namespace,
			Name:       f.Name,
			SourceFile: f.Path,
			Manifest:   string(f.Content),
			BaseBlob:   pinOf(current, ok),
		}
		if f.Kind == model.KindRB {
			e.Resource, e.Namespace = draft.ResourceRoleBinding, c.publishing.PortailNamespace
		}
		entries = append(entries, e)
	}
	grants, err := c.grantSource(commitProj)
	if err != nil {
		return plan, nil, err
	}
	if !pt.RoutesGranted {
		for _, ns := range plan.Reach {
			routes, err := grants.routes(ns, target.Name)
			if err != nil {
				return plan, nil, err
			}
			entries = append(entries, routes...)
		}
	}
	view, err := grants.gatewayView(plan.Namespace, plan.Reach)
	if err != nil {
		return plan, nil, err
	}
	entries = append(entries, view...)
	switch {
	case !pt.PublishNamespace:
		return plan, entries, fmt.Errorf("%w: %s does not exist", ErrNoPublishNamespace, plan.Namespace)
	case len(plan.Reach) == 0:
		return plan, nil, fmt.Errorf("%w (%s)", ErrApart, plan.Namespace)
	case len(entries) == 0:
		return plan, nil, fmt.Errorf("%w for %s", ErrPublishingOn, target.Name)
	case plan.Primary && !pt.Isolated:
		return plan, entries, ErrPrimaryNetwork
	}
	return plan, entries, nil
}
