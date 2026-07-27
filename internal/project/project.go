// Package project resolves dotvirt's tenants from live cluster facts: a project
// is a set of namespaces that share a label (dotvirt.io/project=<name>) and point
// at one git repo via an annotation (dotvirt.io/repo=<url>). There is no dotvirt
// registry - the cluster IS the registry, read with the caller's own token, so a
// user only ever learns the repos of namespaces their RBAC already lets them see.
package project

import (
	"sort"

	"github.com/epheo/dotvirt/pkg/forge"
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
// cluster snapshot (clusterstate), not fetched per request - the resolver is a
// pure function over this set.
type Namespace struct {
	Name        string
	Labels      map[string]string
	Annotations map[string]string
}

// Resolver maps namespaces to projects using a label (project name) and an
// annotation (repo ref). The ref may be host-free ("dotvirt/demo.git"), resolved
// against forgeBase at read time: the forge's identity then lives only in the
// install config, so a forge-host change re-points every project instead of
// stranding them on a dead absolute URL. Absolute refs pass through (BYO forges,
// pre-existing annotations).
type Resolver struct {
	projectLabel string
	repoAnno     string
	forgeBase    string
}

// NewResolver builds a Resolver for the given label/annotation keys. forgeBase is
// the effective forge URL relative repo refs resolve against; empty leaves them
// unresolvable (surfaced as the project's Error).
func NewResolver(projectLabel, repoAnno, forgeBase string) *Resolver {
	return &Resolver{projectLabel: projectLabel, repoAnno: repoAnno, forgeBase: forgeBase}
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
// (nil means "no filter" - the SA/background path, which sees all). A namespace
// not in visible is dropped, so a user never learns a project (or its repo URL)
// outside their RBAC: this filter is the authorization gate. A project whose
// namespaces set no repo, or disagree on it, is returned with Error set and
// Repo empty.
//
// Pure function - no cluster calls. The expensive parts (which namespaces exist,
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
		// Resolve BEFORE deduping, so a relative ref and its absolute form count as
		// one repo rather than a conflict.
		if repo := ns.Annotations[r.repoAnno]; repo != "" {
			a.repos[forge.ResolveRef(r.forgeBase, repo)] = struct{}{}
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
			if info.Repo == "" {
				// A relative ref with no forge to resolve against: listed, not editable.
				info.Error = "repo ref is relative but no forge is configured to resolve it"
			}
		default:
			info.Error = "conflicting dotvirt.io/repo annotations across the project's namespaces"
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
