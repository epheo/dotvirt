package changeset

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/publish"
)

// PublishTarget is a project namespace to turn publishing on in, with what
// the API's snapshots say of it.
type PublishTarget struct {
	Namespace string
	// PrimaryNetwork: the namespace holds a primary UDN. Its gateway would
	// reach portail's controller only through the apiserver, where every
	// gateway shares the per-address poll limits: one tenant could stall the
	// others' configuration.
	PrimaryNetwork bool
	// RoutesGranted: the cluster's admin role already writes HTTPRoutes, so
	// the namespace needs no route grant of its own.
	RoutesGranted bool
}

// ErrPublishingOn answers a namespace whose publishing files the platform
// repo already holds as rendered: nothing to stage.
var ErrPublishingOn = fmt.Errorf("%w: publishing is already on", model.ErrInvalid)

// ErrPrimaryNetwork refuses publishing on a namespace with its own VM
// Network.
var ErrPrimaryNetwork = fmt.Errorf("%w: publishing on a namespace with its own VM Network needs a newer portail: today one such namespace could delay the others' route changes", model.ErrInvalid)

// StagePublishing stages, into the platform draft, turning publishing on in
// one of target's namespaces: its Gateway, its certificate when the platform
// names an issuer, and the HTTPRoute grant where the admin role lacks one.
func (c *Coordinator) StagePublishing(id auth.Identity, commitProj, target project.ProjectInfo, pt PublishTarget) (model.DraftView, error) {
	entries, err := c.publishingEntries(commitProj, target, pt)
	if err != nil {
		return model.DraftView{}, err
	}
	return c.stage(id, commitProj, entries...)
}

// PublishingNeeds is StagePublishing's accesses, unstaged: the error it
// answers when publishing is already on.
func (c *Coordinator) PublishingNeeds(commitProj, target project.ProjectInfo, pt PublishTarget) ([]Access, error) {
	entries, err := c.publishingEntries(commitProj, target, pt)
	if err != nil {
		return nil, err
	}
	return c.needs(commitProj, entries)
}

// publishingEntries is what turning publishing on in pt.Namespace stages
// into commitProj: each file the base branch does not already hold as
// rendered.
func (c *Coordinator) publishingEntries(commitProj, target project.ProjectInfo, pt PublishTarget) ([]draft.Entry, error) {
	if !c.publishing.On() {
		return nil, fmt.Errorf("%w: publishing is not configured on this install", model.ErrUnavailable)
	}
	if err := tenantName(target.Name); err != nil {
		return nil, err
	}
	if !slices.Contains(target.Namespaces, pt.Namespace) {
		return nil, fmt.Errorf("%w: namespace %q is not in project %q", model.ErrInvalid, pt.Namespace, target.Name)
	}
	if pt.PrimaryNetwork {
		return nil, ErrPrimaryNetwork
	}
	files, err := publish.Manifests(c.publishing, pt.Namespace)
	if err != nil {
		return nil, invalid(err)
	}
	read, err := c.read(commitProj)
	if err != nil {
		return nil, err
	}
	var entries []draft.Entry
	for _, f := range files {
		current, ok, err := read.LookupOnBranch(c.baseBranch, f.Path)
		if err != nil {
			return nil, err
		}
		if ok && bytes.Equal(current, f.Content) {
			continue
		}
		entries = append(entries, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourcePublishing,
			Namespace:  pt.Namespace,
			Name:       f.Name,
			SourceFile: f.Path,
			Manifest:   string(f.Content),
			BaseBlob:   pinOf(current, ok),
		})
	}
	if !pt.RoutesGranted {
		grants, err := c.grantSource(commitProj)
		if err != nil {
			return nil, err
		}
		routes, err := grants.routes(pt.Namespace, target.Name)
		if err != nil {
			return nil, err
		}
		entries = append(entries, routes...)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w in %s", ErrPublishingOn, pt.Namespace)
	}
	return entries, nil
}
