package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// visibleSet is a token's visible-namespace set stamped with the RBAC version it was
// derived at; the read is valid only while that version still matches the bus.
type visibleSet struct {
	ns  map[string]bool
	ver uint64
}

// ssarVerdict is one (token, resource) create-SSAR answer, stamped the same way.
type ssarVerdict struct {
	ok  bool
	ver uint64
}

// rbacVersion is the change-version a token's derived authorization depends on: a
// RoleBinding move OR a namespace add/remove/relabel can change what it may see.
func (s *Server) rbacVersion() uint64 {
	return s.bus.Version(eventbus.RBACChanged, eventbus.NamespaceChanged)
}

// This file is every handler's shared preamble: who is calling (identity →
// cluster client), what they may see (visible namespaces → projects), and which
// project a request targets (the pickers).

// userCluster builds the caller's cluster client (identity from context). It
// fails closed: no identity or no factory → an error the caller turns into 401/503.
func (s *Server) userCluster(r *http.Request) (auth.Identity, *cluster.Client, error) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		return auth.Identity{}, nil, fmt.Errorf("no identity")
	}
	if s.clusterF == nil {
		return id, nil, fmt.Errorf("cluster not configured")
	}
	c, err := s.clusterF.For(id.Token)
	return id, c, err
}

// projectsFor resolves the caller's projects: the project topology comes from the
// SA-owned snapshot (shared, no per-request fetch), filtered to the namespaces the
// caller's token may see (TTL-cached per token). The visible set is the sole
// per-user authorization input — a user never learns a project outside their RBAC.
func (s *Server) projectsFor(ctx context.Context, id auth.Identity, c *cluster.Client) ([]project.ProjectInfo, error) {
	visible, err := s.visibleFor(ctx, id, c)
	if err != nil {
		return nil, err
	}
	return s.resolver.Resolve(s.state.Namespaces(), visible), nil
}

// visibleFor returns the set of namespaces id's token may read VMs in, cached by
// token for visibleTTL. The snapshot's project namespaces feed the Forbidden→SSRR
// fallback inside VisibleNamespaces.
func (s *Server) visibleFor(ctx context.Context, id auth.Identity, c *cluster.Client) (map[string]bool, error) {
	ver := s.rbacVersion()
	key := restfactory.TokenKey(id.Token)
	if e, ok := s.visible.Get(key); ok && e.ver == ver {
		return e.ns, nil
	}
	candidates := namespaceNames(s.state.Namespaces())
	names, err := c.VisibleNamespaces(ctx, candidates)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	s.visible.Put(key, visibleSet{ns: set, ver: ver})
	return set, nil
}

// canCreateCached is CanCreateClusterResource behind the per-(token, resource),
// rbacVersion-stamped cache — for authorization signals read on polled or
// broadcast paths, where an uncached SSAR would post to the apiserver per
// request. A RoleBinding/namespace move invalidates lazily via the version
// stamp; the TTL backstops the cluster-scoped RBAC changes the version doesn't
// observe (see visibleTTL). Mutating routes keep their uncached platformScope
// SSAR — a write deserves a fresh answer.
func (s *Server) canCreateCached(ctx context.Context, id auth.Identity, c *cluster.Client, group, resource string) bool {
	ver := s.rbacVersion()
	key := restfactory.TokenKey(id.Token) + "\x00" + group + "/" + resource
	if e, ok := s.ssar.Get(key); ok && e.ver == ver {
		return e.ok
	}
	ok := c.CanCreateClusterResource(ctx, group, resource)
	s.ssar.Put(key, ssarVerdict{ok: ok, ver: ver})
	return ok
}

// platformAuthorResources are the create-SSARs that signal platform-tier
// authoring — one per family of platform create routes. Holding ANY of them
// grants access to the caller's OWN platform draft (drafts are per-user: a
// caller can only view/unstage/propose what their per-route create gates let
// them stage), so the whole-draft routes OR these rather than demanding one
// specific verb; the PR merge review remains the apply boundary.
var platformAuthorResources = []struct{ group, resource string }{
	{"k8s.ovn.org", "clusteruserdefinednetworks"},
	{"operator.openshift.io", "kubedeschedulers"},
	{"template.kubevirt.io", "virtualmachinetemplates"},
}

// canAuthorPlatform reports whether id may author platform-tier changes —
// any platform authoring signal, TTL-cached per token so the inventory
// broadcast path stays free of a per-subscriber cluster call. False when no
// platform repo is configured. Used to seed the synthetic platform project
// into the proposals query so a platform PR surfaces on a cold page load, not
// only right after proposing.
func (s *Server) canAuthorPlatform(ctx context.Context, id auth.Identity, c *cluster.Client) bool {
	if s.cfg.PlatformRepo == "" {
		return false
	}
	for _, r := range platformAuthorResources {
		if s.canCreateCached(ctx, id, c, r.group, r.resource) {
			return true
		}
	}
	return false
}

func namespaceNames(nss []project.Namespace) []string {
	out := make([]string, 0, len(nss))
	for _, ns := range nss {
		out = append(out, ns.Name)
	}
	return out
}

// scope is the per-request context every draft/VM handler resolves up front: the
// caller's identity, their cluster client, and the target project.
type scope struct {
	id      auth.Identity
	cluster *cluster.Client
	proj    project.ProjectInfo
}

// resolveProject is the shared preamble for every draft/VM route: identity → user
// cluster → the caller's projects → pick(projects). pick selects the target
// project (by path namespace, ?project=, or spec namespace) and, when it can't,
// supplies the not-found message. It writes the error status and returns ok=false
// on any failure; handlers just `if !ok { return }`. The returned scope carries the
// cluster client so a handler that needs it (e.g. resync's permission check) reuses
// it instead of re-minting.
func (s *Server) resolveProject(w http.ResponseWriter, r *http.Request, pick projectPicker) (scope, bool) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return scope{}, false
	}
	if s.draft == nil {
		http.Error(w, "changeset/draft not configured", http.StatusServiceUnavailable)
		return scope{}, false
	}
	projects, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return scope{}, false
	}
	proj, msg, ok := pick(projects)
	if !ok {
		http.Error(w, msg, http.StatusNotFound)
		return scope{}, false
	}
	return scope{id: id, cluster: c, proj: proj}, true
}

// projectPicker selects the target project from the caller's visible projects,
// returning a not-found message when none matches.
type projectPicker func([]project.ProjectInfo) (project.ProjectInfo, string, bool)

// byNamespace picks the project owning ns — the per-request authorization point
// for VM routes (a VM in a namespace outside the caller's projects is not found).
func byNamespace(ns string) projectPicker {
	return func(projects []project.ProjectInfo) (project.ProjectInfo, string, bool) {
		for _, p := range projects {
			for _, n := range p.Namespaces {
				if n == ns {
					return p, "", true
				}
			}
		}
		return project.ProjectInfo{}, "namespace not found in any visible project", false
	}
}

// byName picks the project named want (for whole-draft routes carrying ?project=).
func byName(want string) projectPicker {
	return func(projects []project.ProjectInfo) (project.ProjectInfo, string, bool) {
		for _, p := range projects {
			if p.Name == want {
				return p, "", true
			}
		}
		return project.ProjectInfo{}, "project not found or not visible", false
	}
}

// projectByName resolves a project by name from the SA-owned snapshot, WITHOUT the
// caller's RBAC filter. Only safe behind a platform-admin gate (platformScope): it's
// how the platform tier addresses a tenant it's about to adopt, the same all-projects
// view the exporter and ApplicationSet use.
func (s *Server) projectByName(name string) (project.ProjectInfo, bool) {
	for _, p := range s.resolver.Resolve(s.state.Namespaces(), nil) {
		if p.Name == name {
			return p, true
		}
	}
	return project.ProjectInfo{}, false
}

// draftScope resolves the whole-draft routes (GET/DELETE/propose) that carry the
// project via ?project= rather than a VM namespace.
func (s *Server) draftScope(w http.ResponseWriter, r *http.Request) (scope, bool) {
	want := r.URL.Query().Get("project")
	if want == "" {
		http.Error(w, "project query parameter is required", http.StatusBadRequest)
		return scope{}, false
	}
	return s.pickProject(w, r, want)
}

// pickProject resolves a project named by a ?project= query or {project} path
// segment (the whole-project routes: draft, propose, unstage, history, revert).
// The platform tier resolves ONLY for callers who can author platform changes
// (any signal in platformAuthorResources — the routes touch the caller's own
// draft, staged behind those same per-route gates), so a plain tenant cannot
// reach the platform repo's draft, history, or revert. Any other name resolves
// from the caller's visible projects.
func (s *Server) pickProject(w http.ResponseWriter, r *http.Request, want string) (scope, bool) {
	if want == platformProjectName {
		return s.platformScopeAny(w, r)
	}
	return s.resolveProject(w, r, byName(want))
}

// platformScopeAny resolves the platform tier for the whole-draft routes: any
// platform authoring signal suffices (see platformAuthorResources). The
// per-kind create routes keep platformScope's exact-resource gate.
func (s *Server) platformScopeAny(w http.ResponseWriter, r *http.Request) (scope, bool) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return scope{}, false
	}
	if s.draft == nil {
		http.Error(w, "changeset/draft not configured", http.StatusServiceUnavailable)
		return scope{}, false
	}
	if s.cfg.PlatformRepo == "" {
		http.Error(w, "platform repo not configured (set -platform-repo)", http.StatusServiceUnavailable)
		return scope{}, false
	}
	if !s.canAuthorPlatform(r.Context(), id, c) {
		http.Error(w, "not authorized to author platform changes", http.StatusForbidden)
		return scope{}, false
	}
	return scope{id: id, cluster: c, proj: project.ProjectInfo{Name: platformProjectName, Repo: s.cfg.PlatformRepo}}, true
}

// platformProjectName is the synthetic project name for the platform tier — the
// repo holding cluster-scoped + tenancy manifests. It is config-only (-platform-repo),
// never a dotvirt.io/project-labeled namespace, so project discovery never emits it.
const platformProjectName = "platform"

// platformScope resolves the platform tier for a cluster-scoped create: the
// caller's identity + cluster, and the synthetic platform ProjectInfo from
// -platform-repo. It SSAR-gates on the caller's authority to create group/resource —
// the authoring SIGNAL (the user never applies it; Argo does, from the platform
// repo), so the author-time check matches the apply-time AppProject boundary. Fails
// closed: 503 if no platform repo is configured, 403 if the caller lacks the verb.
func (s *Server) platformScope(w http.ResponseWriter, r *http.Request, group, resource string) (scope, bool) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return scope{}, false
	}
	if s.draft == nil {
		http.Error(w, "changeset/draft not configured", http.StatusServiceUnavailable)
		return scope{}, false
	}
	if s.cfg.PlatformRepo == "" {
		http.Error(w, "platform repo not configured (set -platform-repo)", http.StatusServiceUnavailable)
		return scope{}, false
	}
	if !c.CanCreateClusterResource(r.Context(), group, resource) {
		http.Error(w, "not authorized to create "+resource, http.StatusForbidden)
		return scope{}, false
	}
	return scope{id: id, cluster: c, proj: project.ProjectInfo{Name: platformProjectName, Repo: s.cfg.PlatformRepo}}, true
}
