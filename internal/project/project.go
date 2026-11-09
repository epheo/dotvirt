// Package project resolves dotvirt's tenants from live cluster facts: a project
// is a set of namespaces that share a label (dotvirt.io/project=<name>) and point
// at one git repo via an annotation (dotvirt.io/repo=<url>). There is no dotvirt
// registry - the cluster IS the registry, read with the caller's own token, so a
// user only ever learns the repos of namespaces their RBAC already lets them see.
package project

import (
	"log"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/epheo/dotvirt/pkg/forge"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// ProjectInfo is one resolved tenant: its name, the repo backing it, the
// namespaces that make it up, and an Error when those namespaces don't agree on a
// usable repo (project still listed, but not editable).
type ProjectInfo struct {
	Name       string
	Repo       string
	Path       string // folder of Repo the project lives in; "" is all of it
	Namespaces []string
	Error      string
}

// Namespace is the input to resolution: a project-labeled namespace's name plus
// the label/annotation maps the resolver reads. It is supplied from the SA-owned
// cluster snapshot (clusterstate), not fetched per request - the resolver is a
// pure function over this set. Created weighs it as an Application destination
// (tenancy.Destination).
type Namespace struct {
	Name        string
	Labels      map[string]string
	Annotations map[string]string
	Created     time.Time
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
	warned       sync.Map // namespace -> struct{}: reserved-name labels already logged
}

// PlatformName is the synthetic platform tier's project name. It is reserved:
// a tenant by that name would share the platform draft and its route gates, so
// Resolve never emits one and the tenancy flows refuse it.
const PlatformName = tenancy.PlatformProject

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
	paths      map[string]struct{}
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
	shared := r.sharedRepos(namespaces)
	byProject := map[string]*accum{}
	for _, ns := range namespaces {
		if visible != nil && !visible[ns.Name] {
			continue // outside the caller's RBAC: never surface it
		}
		name := ns.Labels[r.projectLabel]
		if name == "" {
			continue // not a dotvirt-managed namespace
		}
		if name == PlatformName {
			if _, seen := r.warned.LoadOrStore(ns.Name, struct{}{}); !seen {
				log.Printf("project: namespace %s carries the reserved project name %q; ignored", ns.Name, PlatformName)
			}
			continue
		}
		a := byProject[name]
		if a == nil {
			a = &accum{repos: map[string]struct{}{}, paths: map[string]struct{}{}}
			byProject[name] = a
		}
		a.namespaces = append(a.namespaces, ns.Name)
		// Resolve BEFORE deduping, so a relative ref and its absolute form count as
		// one repo rather than a conflict.
		if repo := ns.Annotations[r.repoAnno]; repo != "" {
			a.repos[forge.ResolveRef(r.forgeBase, repo)] = struct{}{}
		}
		// Absent is no opinion, like the repo: a namespace added later need not repeat it.
		if p, ok := ns.Annotations[tenancy.PathAnnotation]; ok {
			a.paths[p] = struct{}{}
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
		if info.Error == "" {
			info.Path, info.Error = a.path()
		}
		if info.Error == "" && shared[info.Repo] {
			info.Error = "this repo is also another project's: a folder does not separate tenants, give each project its own repo"
		}
		if info.Error != "" {
			info.Repo, info.Path = "", ""
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// path is the project's one folder, or why it has none.
func (a *accum) path() (string, string) {
	if len(a.paths) > 1 {
		return "", "conflicting dotvirt.io/path annotations across the project's namespaces"
	}
	for p := range a.paths {
		clean, ok := CleanPath(p)
		if !ok {
			return "", "invalid dotvirt.io/path annotation"
		}
		return clean, ""
	}
	return "", ""
}

// CleanPath is the repo folder p names, "" for the whole repo. ok=false for
// one that could leave the repo or reach into its metadata.
func CleanPath(p string) (string, bool) {
	if p == "" || p == "." {
		return "", true
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "", false
	}
	clean := path.Clean(p)
	if clean == "." {
		return "", true
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." || seg == ".git" {
			return "", false
		}
	}
	return clean, true
}

// sharedRepos are the repos more than one project names where any of them uses
// a folder: Argo and the forge grant by repo, so whoever may change one folder
// may change the other. Read across every namespace, whatever the caller may
// see. Projects sharing a repo with no folder at all are left as they were.
func (r *Resolver) sharedRepos(namespaces []Namespace) map[string]bool {
	type use struct {
		projects map[string]bool
		folder   bool
	}
	byRepo := map[string]*use{}
	for _, ns := range namespaces {
		name, ref := ns.Labels[r.projectLabel], ns.Annotations[r.repoAnno]
		if name == "" || name == PlatformName || ref == "" {
			continue
		}
		repo := forge.ResolveRef(r.forgeBase, ref)
		u := byRepo[repo]
		if u == nil {
			u = &use{projects: map[string]bool{}}
			byRepo[repo] = u
		}
		u.projects[name] = true
		if p, _ := CleanPath(ns.Annotations[tenancy.PathAnnotation]); p != "" {
			u.folder = true
		}
	}
	shared := map[string]bool{}
	for repo, u := range byRepo {
		if len(u.projects) > 1 && u.folder {
			shared[repo] = true
		}
	}
	return shared
}

// Owners maps every project-labeled namespace to its project, unfiltered: what
// a repo-declared object's namespace belongs to, whoever is asking.
func (r *Resolver) Owners(namespaces []Namespace) map[string]string {
	out := make(map[string]string, len(namespaces))
	for _, ns := range namespaces {
		if name := ns.Labels[r.projectLabel]; name != "" && name != PlatformName {
			out[ns.Name] = name
		}
	}
	return out
}
