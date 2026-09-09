package api

import (
	"context"
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// The object routes act on one git-declared network-family object by the
// identity its draft entry carries: /api/objects/{resource}/{namespace}/{name},
// namespace "cluster" for a cluster-scoped one. They resolve their tier exactly
// as the matching create route does - a namespaced object to the tenant project
// owning its namespace, a cluster-scoped one to the platform repo gated on the
// caller's authority to create that kind - so reading back or deleting an object
// needs the same standing as authoring it.

// clusterResourceSSAR gates each cluster-scoped resource on its create authority.
var clusterResourceSSAR = map[draft.Resource]ssarRef{
	draft.ResourceNetwork:                    ssarCUDN,
	draft.ResourceUplink:                     ssarUplink,
	draft.ResourceEgressIP:                   ssarEgressIP,
	draft.ResourceExternalRoute:              ssarExtRoute,
	draft.ResourceAdminNetworkPolicy:         ssarANP,
	draft.ResourceBaselineAdminNetworkPolicy: ssarBANP,
}

// namespacedResources are the network-family resources a tenant project declares.
var namespacedResources = map[draft.Resource]bool{
	draft.ResourceNetwork:        true,
	draft.ResourceEgressFirewall: true,
	draft.ResourceNetworkPolicy:  true,
}

// objectScope is the preamble of every object route: the resource, its
// identity, and the tier resolved from its scope.
func (s *Server) objectScope(w http.ResponseWriter, r *http.Request) (sc scope, resource, ns, name string, ok bool) {
	resource, ns, name = r.PathValue("resource"), r.PathValue("namespace"), r.PathValue("name")
	res := draft.Resource(resource)
	if ns == changeset.ClusterScopeNS {
		ref, cluster := clusterResourceSSAR[res]
		if !cluster {
			http.Error(w, resource+" is not cluster-scoped", http.StatusBadRequest)
			return sc, "", "", "", false
		}
		sc, ok = s.platformScope(w, r, ref)
		return sc, resource, ns, name, ok
	}
	if !namespacedResources[res] {
		http.Error(w, resource+" is not namespace-scoped", http.StatusBadRequest)
		return sc, "", "", "", false
	}
	sc, ok = s.resolveProject(w, r, byNamespace(ns))
	return sc, resource, ns, name, ok
}

// handleObjectSpec reads a declared object back as the spec its form edits.
func (s *Server) handleObjectSpec(w http.ResponseWriter, r *http.Request) {
	sc, resource, ns, name, ok := s.objectScope(w, r)
	if !ok {
		return
	}
	spec, err := s.draft.ObjectSpec(sc.proj, resource, ns, name)
	respond(w, spec, err)
}

// handleObjectDelete stages the removal of a declared object's manifest - the
// same draft-only path as a VM delete; Argo prunes the object on merge.
func (s *Server) handleObjectDelete(w http.ResponseWriter, r *http.Request) {
	sc, resource, ns, name, ok := s.objectScope(w, r)
	if !ok {
		return
	}
	view, err := s.draft.StageDelete(sc.id, sc.proj, resource, ns, name)
	respond(w, view, err)
}

// sourceFiles answers "which file declares this object" across the caller's
// projects, the platform repo included when withPlatform - the read plane's
// signal that an object can be edited or deleted from here. Each project's index
// is fetched once per call, and an unreachable repo reads as declaring nothing.
func (s *Server) sourceFiles(ctx context.Context, id auth.Identity, c *cluster.Client, withPlatform bool) func(kind, namespace, name string) string {
	projects, err := s.projectsFor(ctx, id, c)
	if err != nil {
		return func(string, string, string) string { return "" }
	}
	byNS := map[string]project.ProjectInfo{}
	for _, p := range projects {
		for _, ns := range p.Namespaces {
			byNS[ns] = p
		}
	}
	indexes := map[string]map[model.ObjectRef]string{}
	index := func(p project.ProjectInfo) map[model.ObjectRef]string {
		if idx, ok := indexes[p.Name]; ok {
			return idx
		}
		var idx map[model.ObjectRef]string
		if p.Error == "" { // a project the resolver flagged has no repo to read
			idx, _ = s.draft.DeclaredFiles(p)
		}
		indexes[p.Name] = idx
		return idx
	}
	return func(kind, namespace, name string) string {
		var p project.ProjectInfo
		if namespace == "" {
			if !withPlatform || s.cfg.PlatformRepo == "" {
				return ""
			}
			p = s.platformProject()
		} else if p = byNS[namespace]; p.Repo == "" {
			return ""
		}
		return index(p)[model.ObjectRef{Kind: kind, Namespace: namespace, Name: name}]
	}
}
