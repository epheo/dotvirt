package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/restfactory"
)

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
	if v, ok := s.visible.Get(restfactory.TokenKey(id.Token)); ok {
		return v, nil
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
	s.visible.Put(restfactory.TokenKey(id.Token), set)
	return set, nil
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
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return scope{}, false
	}
	if s.draft == nil {
		http.Error(w, "changeset/draft not configured", http.StatusServiceUnavailable)
		return scope{}, false
	}
	projects, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

// draftScope resolves the whole-draft routes (GET/DELETE/propose) that carry the
// project via ?project= rather than a VM namespace.
func (s *Server) draftScope(w http.ResponseWriter, r *http.Request) (scope, bool) {
	want := r.URL.Query().Get("project")
	if want == "" {
		http.Error(w, "project query parameter is required", http.StatusBadRequest)
		return scope{}, false
	}
	return s.resolveProject(w, r, byName(want))
}
