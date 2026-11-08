package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// The object routes act on one git-declared network-family object by the
// identity its draft entry carries: /api/objects/{resource}/{namespace}/{name},
// namespace "cluster" for a cluster-scoped one. They resolve their tier exactly
// as the matching create route does - a namespaced object to the tenant project
// owning its namespace, a cluster-scoped one to the platform repo - and a
// write opens on its own verb over the resource's kinds (objectActions).

// objectScope is the preamble of every object route: the resource, its
// identity, and the tier resolved from its scope in the kind table. verbs are
// the write's, any of which opens it; none is a read, for which a
// cluster-scoped object needs the authority to author its kind and a tenant
// one only the namespace's visibility.
func (s *Server) objectScope(w http.ResponseWriter, r *http.Request, verbs ...string) (sc scope, resource, ns, name string, ok bool) {
	resource, ns, name = r.PathValue("resource"), r.PathValue("namespace"), r.PathValue("name")
	res, known := model.LookupResource(resource)
	if !known || !res.NetworkFamily {
		fail(w, invalid(fmt.Errorf("%s has no object routes", resource)))
		return sc, "", "", "", false
	}
	if ns == model.ClusterScopeNS {
		kind, clusterScoped := res.ClusterKind()
		if !clusterScoped {
			fail(w, invalid(fmt.Errorf("%s is not cluster-scoped", resource)))
			return sc, "", "", "", false
		}
		if len(verbs) == 0 {
			sc, ok = s.platformScope(w, r, ssarFor(kind))
		} else {
			sc, ok = s.platformScopeWith(w, r, func(ctx context.Context, id auth.Identity, c *cluster.Client) (bool, error) {
				return s.anyAllowed(ctx, id, c, objectActions(verbs, resource, ns))
			}, fmt.Sprintf("not authorized to %s %s", strings.Join(verbs, " or "), kind.Plural))
		}
		return sc, resource, ns, name, ok
	}
	if !res.Namespaced() {
		fail(w, invalid(fmt.Errorf("%s is not namespace-scoped", resource)))
		return sc, "", "", "", false
	}
	sc, ok = s.resolveProject(w, r, byNamespace(ns))
	if ok && len(verbs) > 0 {
		ok = s.gateAny(w, r, sc, objectActions(verbs, resource, ns))
	}
	return sc, resource, ns, name, ok
}

// handleObjectSpec reads a declared object back as the spec its form edits.
func (s *Server) handleObjectSpec(w http.ResponseWriter, r *http.Request) {
	sc, resource, ns, name, ok := s.objectScope(w, r)
	if !ok {
		return
	}
	spec, err := s.reader.ObjectSpec(sc.proj, resource, ns, name)
	respond(w, spec, err)
}

// handleObjectUpdateManifest stages a declared object's manifest replaced
// verbatim: the edit for what its form has no field for.
func (s *Server) handleObjectUpdateManifest(w http.ResponseWriter, r *http.Request) {
	sc, resource, ns, name, ok := s.objectScope(w, r, verbUpdate)
	if !ok {
		return
	}
	req, ok := decode[model.UpdateManifestRequest](w, r)
	if !ok {
		return
	}
	if req.YAML == "" {
		fail(w, invalid(errors.New("yaml is required")))
		return
	}
	view, err := s.draft.StageUpdateManifest(sc.id, sc.proj, resource, ns, name, req)
	respond(w, view, err)
}

// handleObjectDelete stages the removal of a declared object's manifest - the
// same draft-only path as a VM delete; Argo prunes the object on merge.
func (s *Server) handleObjectDelete(w http.ResponseWriter, r *http.Request) {
	sc, resource, ns, name, ok := s.objectScope(w, r, verbDelete)
	if !ok {
		return
	}
	view, err := s.draft.StageDelete(sc.id, sc.proj, resource, ns, name)
	respond(w, view, err)
}

// handleObjectAdopt stages one running object: the namespace (or cluster)
// capture, narrowed to the object asked for. Adopting creates the declaring
// file, or edits it when git describes the object otherwise, so either right
// opens the route.
func (s *Server) handleObjectAdopt(w http.ResponseWriter, r *http.Request) {
	sc, resource, ns, name, ok := s.objectScope(w, r, verbCreate, verbUpdate)
	if !ok {
		return
	}
	objs, unreadable, ok := s.captureAdoptable(w, r, sc, model.ObjectNamespace(ns))
	if !ok {
		return
	}
	kinds := draft.Resource(resource).Kinds()
	var picked []model.Adoptable
	for _, o := range objs {
		if o.Name == name && slices.Contains(kinds, o.Kind) {
			picked = append(picked, o)
		}
	}
	if len(picked) != 1 {
		fail(w, fmt.Errorf("%w: %s/%s is not running", model.ErrNotFound, ns, name))
		return
	}
	result, err := s.draft.AdoptObject(sc.id, sc.proj, picked[0])
	respond(w, withUnreadable(result, ns, unreadable, nil), err)
}

// handleObjectHistory lists the merged changes to one declared object's file.
func (s *Server) handleObjectHistory(w http.ResponseWriter, r *http.Request) {
	sc, resource, ns, name, ok := s.objectScope(w, r)
	if !ok {
		return
	}
	commits, err := s.reader.ObjectHistory(sc.proj, resource, ns, name, 10)
	respond(w, commits, err)
}

// handleObjectRestore stages one declared object's file as a past commit held it.
func (s *Server) handleObjectRestore(w http.ResponseWriter, r *http.Request) {
	sc, resource, ns, name, ok := s.objectScope(w, r, verbUpdate)
	if !ok {
		return
	}
	hash, ok := hashBody(w, r)
	if !ok {
		return
	}
	result, err := s.draft.RestoreVersion(sc.id, sc.proj, resource, ns, name, hash)
	respond(w, result, err)
}

// handlePlatformAdopt stages every cluster-scoped object the platform repo does
// not describe - the platform tier's counterpart of the namespace adoption -
// of the kinds the caller may create.
func (s *Server) handlePlatformAdopt(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.platformScopeAny(w, r)
	if !ok {
		return
	}
	objs, unreadable, ok := s.captureAdoptable(w, r, sc, "")
	if !ok {
		return
	}
	objs, denied, ok := s.adoptableByRight(w, r, sc, "", objs)
	if !ok {
		return
	}
	result, err := s.draft.AdoptObjects(sc.id, sc.proj, "the cluster scope", objs)
	respond(w, withUnreadable(result, "cluster scope", unreadable, denied), err)
}

// sourceFiles answers "which file declares this object" across the caller's
// projects, the platform repo included when withPlatform - the read plane's
// signal that an object can be edited or deleted from here. The repo's
// declared index is memoized per branch head, so a lookup is a cache read. A
// project the resolver flagged has no repo to read.
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
		if p.Error != "" {
			return ""
		}
		files, err := s.reader.DeclaredFiles(p)
		if err != nil {
			return ""
		}
		return files[model.ObjectRef{Kind: kind, Namespace: namespace, Name: name}]
	}
}
