package api

// Tenancy: creating namespaces, bootstrapping tenant projects, and adopting
// repoless ones. All platform-admin acts (they land Namespace + RBAC objects in
// the platform tier), kept apart from the port-group handlers next door.

import (
	"encoding/json"
	"net/http"
)

// handleCreateNamespace stages a new namespace (+ optional primary "VM Network").
// The Namespace object is cluster-scoped, so it is COMMITTED to the platform repo
// and gated on namespace-create authority; but it is labeled/annotated to the tenant
// project it JOINS (carried as "project"), so that project's Argo app syncs
// workloads into it once the platform app creates it.
func (s *Server) handleCreateNamespace(w http.ResponseWriter, r *http.Request) {
	raw, p, ok := peek[struct {
		Project string `json:"project"`
	}](w, r)
	if !ok {
		return
	}
	if p.Project == "" {
		http.Error(w, "the project the namespace joins is required", http.StatusBadRequest)
		return
	}
	// The tenant project it joins: annotation source + authz (the caller must see it).
	join, ok := s.resolveProject(w, r, byName(p.Project))
	if !ok {
		return
	}
	// Commit to the platform tier, gated on namespace-create authority.
	plat, ok := s.platformScope(w, r, ssarNamespace)
	if !ok {
		return
	}
	view, err := s.draft.StageCreateNamespace(join.id, plat.proj, join.proj, raw)
	respond(w, view, err)
}

// handleCreateProject bootstraps a new tenant project from the UI — the "New
// Project" flow. It creates the project's forge repo and stages its first namespace
// (+ an optional owners RoleBinding) into the platform repo. Gated on the same
// namespace-create authority as handleCreateNamespace: creating a tenant is a
// platform-admin act (it lands a Namespace + RBAC in the platform tier).
func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	// Peek the name for an early 400; the Coordinator re-decodes the full spec.
	raw, p, ok := peek[struct {
		Name string `json:"name"`
	}](w, r)
	if !ok {
		return
	}
	if p.Name == "" {
		http.Error(w, "a project name is required", http.StatusBadRequest)
		return
	}
	plat, ok := s.platformScope(w, r, ssarNamespace)
	if !ok {
		return
	}
	// The cluster is dotvirt's registry, so an existing project is what makes this a
	// re-manage rather than a create. Checked here, before the coordinator touches the
	// forge, so a refusal never leaves a repo behind.
	if _, exists := s.projectByName(p.Name); exists {
		http.Error(w, "that project already exists; adopt it instead of creating it", http.StatusConflict)
		return
	}
	view, err := s.draft.StageCreateProject(plat.id, plat.proj, raw)
	respond(w, view, err)
}

// handleAdoptProject wires a repo to an existing labeled-but-repoless project — the
// "Attach repo" action on the inventory's no-repo dead-end. Like handleCreateProject
// it's a platform-admin act (it lands a Namespace + repo annotation in the platform
// tier), so it's gated on namespace-create authority; the target tenant is resolved
// from the SA snapshot (the caller is a platform admin) and must currently be repoless.
func (s *Server) handleAdoptProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Owners []string `json:"owners,omitempty"`
	}
	if raw, err := readAll(r); err != nil {
		fail(w, invalid(err))
		return
	} else if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			fail(w, invalid(err))
			return
		}
	}
	plat, ok := s.platformScope(w, r, ssarNamespace)
	if !ok {
		return
	}
	target, ok := s.projectByName(r.PathValue("project"))
	if !ok {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	view, err := s.draft.AdoptProject(plat.id, plat.proj, target, body.Owners)
	respond(w, view, err)
}
