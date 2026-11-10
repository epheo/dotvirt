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
)

// PublishTarget is what the API's snapshots say of a project's namespaces.
type PublishTarget struct {
	// Networks is each namespace's primary network, "default" for the
	// cluster's own: namespaces sharing one reach each other's Services.
	Networks map[string]string
	// RoutesGranted: the cluster's admin role already writes HTTPRoutes, so
	// no namespace needs a route grant of its own.
	RoutesGranted bool
}

// DefaultNetwork is the cluster's own network in PublishTarget.Networks.
const DefaultNetwork = "default"

// PublishPlan is where a project's Gateway goes and whom it serves.
type PublishPlan struct {
	// Namespace holds the Gateway: the one git holds it in, else the first
	// namespace of the project's largest network.
	Namespace string
	// Reach is the project's namespaces on that network, Namespace among
	// them. One with a VM Network of its own is not: no gateway joins two
	// primary networks.
	Reach []string
	// On: git holds the project's Gateway.
	On bool
	// Primary: that network is a VM Network, whose pods reach portail's
	// controller only through the apiserver.
	Primary bool
}

// ErrPublishingOn answers a project whose publishing the platform repo holds
// as rendered: nothing to stage.
var ErrPublishingOn = fmt.Errorf("%w: publishing is already on", model.ErrInvalid)

// ErrPrimaryNetwork refuses publishing for a project on a VM Network.
var ErrPrimaryNetwork = fmt.Errorf("%w: publishing on a VM Network needs a newer portail: today one project could delay the others' route changes", model.ErrInvalid)

// StagePublishing stages, into the platform draft, what target's publishing
// lacks: its Gateway, its certificate when the platform names an issuer, and
// the HTTPRoute grant of each namespace it reaches where the admin role lacks
// one.
func (c *Coordinator) StagePublishing(id auth.Identity, commitProj, target project.ProjectInfo, pt PublishTarget) (model.DraftView, error) {
	_, entries, err := c.publishPlan(commitProj, target, pt)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.stage(id, commitProj, entries...)
}

// PublishingNeeds is StagePublishing's plan and accesses, unstaged. The plan
// stands beside ErrPublishingOn; beside ErrPrimaryNetwork the accesses are
// what it would ask, so a caller who could is told what it waits on.
func (c *Coordinator) PublishingNeeds(commitProj, target project.ProjectInfo, pt PublishTarget) (PublishPlan, []Access, error) {
	plan, entries, err := c.publishPlan(commitProj, target, pt)
	if err != nil && !errors.Is(err, ErrPrimaryNetwork) {
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
	for _, ns := range target.Namespaces {
		if _, ok, err := read.LookupOnBranch(c.baseBranch, model.KindGateway.RepoPath(ns, publish.Gateway)); err != nil {
			return plan, nil, err
		} else if ok && plan.On {
			return plan, nil, fmt.Errorf("%w: project %q publishes from both %s and %s; keep one", model.ErrConflict, target.Name, plan.Namespace, ns)
		} else if ok {
			plan.Namespace, plan.On = ns, true
		}
	}
	network := func(ns string) string { return cmp.Or(pt.Networks[ns], DefaultNetwork) }
	if !plan.On {
		plan.Namespace = largestNetwork(target.Namespaces, network)
	}
	for _, ns := range slices.Sorted(slices.Values(target.Namespaces)) {
		if network(ns) == network(plan.Namespace) {
			plan.Reach = append(plan.Reach, ns)
		}
	}
	plan.Primary = network(plan.Namespace) != DefaultNetwork
	files, err := publish.Manifests(c.publishing, target.Name, plan.Namespace)
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
		entries = append(entries, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourcePublishing,
			Namespace:  plan.Namespace,
			Name:       f.Name,
			SourceFile: f.Path,
			Manifest:   string(f.Content),
			BaseBlob:   pinOf(current, ok),
		})
	}
	if !pt.RoutesGranted {
		grants, err := c.grantSource(commitProj)
		if err != nil {
			return plan, nil, err
		}
		for _, ns := range plan.Reach {
			routes, err := grants.routes(ns, target.Name)
			if err != nil {
				return plan, nil, err
			}
			entries = append(entries, routes...)
		}
	}
	if len(entries) == 0 {
		return plan, nil, fmt.Errorf("%w for %s", ErrPublishingOn, target.Name)
	}
	if plan.Primary {
		return plan, entries, ErrPrimaryNetwork
	}
	return plan, entries, nil
}

// largestNetwork is the first namespace, by name, of the network most of
// namespaces share; a tie goes to the network of the first namespace.
func largestNetwork(namespaces []string, network func(string) string) string {
	sorted := slices.Sorted(slices.Values(namespaces))
	count := map[string]int{}
	for _, ns := range sorted {
		count[network(ns)]++
	}
	best := sorted[0]
	for _, ns := range sorted {
		if count[network(ns)] > count[network(best)] {
			best = ns
		}
	}
	return best
}
