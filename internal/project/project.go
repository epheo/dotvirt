// Package project resolves dotvirt's tenants from live cluster facts: a project
// is a set of namespaces that share a label (dotvirt.io/project=<name>) and point
// at one git repo via an annotation (dotvirt.io/repo=<url>). There is no dotvirt
// registry — the cluster IS the registry, read with the caller's own token, so a
// user only ever learns the repos of namespaces their RBAC already lets them see.
package project

import (
	"sort"
)

// ProjectInfo is one resolved tenant: its name, the repo backing it, the
// namespaces that make it up, and an Error when those namespaces don't agree on a
// usable repo (project still listed, but not editable).
type ProjectInfo struct {
	Name       string
	Repo       string
	Namespaces []string
	Error      string
}

// Namespace is the input to resolution: a project-labeled namespace's name plus
// the label/annotation maps the resolver reads. It is supplied from the SA-owned
// cluster snapshot (clusterstate), not fetched per request — the resolver itself
// is now a pure function over this set.
type Namespace struct {
	Name        string
	Labels      map[string]string
	Annotations map[string]string
}

// Resolver maps namespaces to projects using a label (project name) and an
// annotation (repo URL).
type Resolver struct {
	projectLabel string
	repoAnno     string
}

// NewResolver builds a Resolver for the given label/annotation keys.
func NewResolver(projectLabel, repoAnno string) *Resolver {
	return &Resolver{projectLabel: projectLabel, repoAnno: repoAnno}
}

// accum gathers a project's member namespaces and the distinct repo URLs they
// annotate, before deciding on the project's single repo (or an Error).
type accum struct {
	namespaces []string
	repos      map[string]struct{}
}

// Resolve groups the project-labeled namespaces into projects, keeping only those
// the caller may see. namespaces is the SA-owned snapshot of every labeled
// namespace (clusterstate); visible is the set the caller's token can read VMs in
// (nil means "no filter" — the SA/background path, which sees all). A namespace
// not in visible is dropped, so a user never learns a project (or its repo URL)
// outside their RBAC: this filter is the authorization gate, replacing the former
// per-token namespace GETs. A project whose namespaces set no repo, or disagree on
// it, is returned with Error set and Repo empty.
//
// Pure function — no cluster calls. The expensive parts (which namespaces exist,
// what they're labeled) come from the shared snapshot; the only per-user input is
// the visible set, computed once per token and cached by the caller.
func (r *Resolver) Resolve(namespaces []Namespace, visible map[string]bool) []ProjectInfo {
	byProject := map[string]*accum{}
	for _, ns := range namespaces {
		if visible != nil && !visible[ns.Name] {
			continue // outside the caller's RBAC: never surface it
		}
		name := ns.Labels[r.projectLabel]
		if name == "" {
			continue // not a dotvirt-managed namespace
		}
		a := byProject[name]
		if a == nil {
			a = &accum{repos: map[string]struct{}{}}
			byProject[name] = a
		}
		a.namespaces = append(a.namespaces, ns.Name)
		if repo := ns.Annotations[r.repoAnno]; repo != "" {
			a.repos[repo] = struct{}{}
		}
	}

	out := make([]ProjectInfo, 0, len(byProject))
	for name, a := range byProject {
		sort.Strings(a.namespaces)
		info := ProjectInfo{Name: name, Namespaces: a.namespaces}
		switch len(a.repos) {
		case 0:
			info.Error = "no repo configured (set the dotvirt.io/repo annotation)"
		case 1:
			for repo := range a.repos {
				info.Repo = repo
			}
		default:
			info.Error = "conflicting dotvirt.io/repo annotations across the project's namespaces"
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
