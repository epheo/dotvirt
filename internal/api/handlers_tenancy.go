package api

// Tenancy: creating namespaces, bootstrapping tenant projects, and adopting
// repoless ones. Each lands Namespace + RBAC objects in the platform tier: the
// route gates its claim on each namespace (claimNamespaces), the coordinator
// each object the change writes. Kept apart from the network handlers next
// door.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// claimNamespaces binds the namespace capture to the caller's token and gates
// the claim on each namespace there, where its existence is known: one that
// already exists is adopted as the caller can read it, never regenerated, and
// needs the right to update it (claimAction), as the coordinator weighs its
// Namespace object. Only then is one the SA snapshot holds for another
// project - the snapshot the coordinator places namespaces by - refused:
// joining would take it from that project, whose Application then fails on
// everything it declares there. That project is named only to a caller who
// sees it.
func (s *Server) claimNamespaces(ctx context.Context, sc scope, project string) changeset.LiveNamespaces {
	return func(name string) (*netgen.LiveNamespace, error) {
		live, err := sc.cluster.LiveNamespace(ctx, name)
		if err != nil || live == nil {
			return nil, err
		}
		if err := s.require(ctx, sc.id, sc.cluster, claimAction(name)); err != nil {
			return nil, err
		}
		held := s.state.NamespaceLabels(name)[s.cfg.ProjectLabel]
		if held == "" || held == project {
			return live, nil
		}
		if projects, err := s.projectsFor(ctx, sc.id, sc.cluster); err == nil {
			if _, seen := findProject(projects, held); seen {
				return nil, fmt.Errorf("%w: namespace %s belongs to project %s; release it there first", model.ErrConflict, name, held)
			}
		}
		return nil, fmt.Errorf("%w: namespace %s belongs to another project", model.ErrConflict, name)
	}
}

// tenancyTarget resolves the project an adopt, release or sync-access route
// names: from every project when the caller may relabel any namespace
// (claimAction("")), else from the projects they see, whole. ok=false means
// the response is written: the same 404 for a project hidden as for none, an
// unnamed 403 for one with a namespace hidden.
func (s *Server) tenancyTarget(w http.ResponseWriter, r *http.Request, sc scope) (project.ProjectInfo, bool) {
	name := r.PathValue("project")
	var target project.ProjectInfo
	found := false
	if s.capable(r.Context(), sc.id, sc.cluster, claimAction("")) {
		target, found = s.projectByName(name)
	} else if projects, err := s.projectsFor(r.Context(), sc.id, sc.cluster); err != nil {
		fail(w, err)
		return project.ProjectInfo{}, false
	} else if visible, ok := findProject(projects, name); ok {
		// The claim and the gate weigh every namespace, and would name one the
		// caller cannot see: such a project is refused without naming it.
		if s.seesPartOf(visible) {
			fail(w, fmt.Errorf("%w: this needs every namespace of %s in your view", model.ErrForbidden, name))
			return project.ProjectInfo{}, false
		}
		target, found = s.projectByName(visible.Name)
	}
	if !found {
		fail(w, fmt.Errorf("%w: project %q", model.ErrNotFound, name))
		return project.ProjectInfo{}, false
	}
	return target, true
}

// handleCreateNamespace stages a new namespace (+ optional primary "VM Network").
// The Namespace object is cluster-scoped, so it is COMMITTED to the platform repo;
// but it is labeled/annotated to the tenant project it JOINS (carried as
// "project"), so that project's Argo app syncs workloads into it once the
// platform app creates it. The route gates the claim on the namespace, decided
// once the capture knows whether it exists.
func (s *Server) handleCreateNamespace(w http.ResponseWriter, r *http.Request) {
	raw, p, ok := readBody[struct {
		Project        string    `json:"project"`
		VMNetwork      *struct{} `json:"vmNetwork"`
		ProjectNetwork bool      `json:"projectNetwork"`
	}](w, r, false)
	if !ok {
		return
	}
	if p.Project == "" {
		fail(w, invalid(errors.New("the project the namespace joins is required")))
		return
	}
	if p.VMNetwork != nil && !s.gateServed(w, r, model.KindUDN) {
		return
	}
	// The tenant project it joins: annotation source + authz (the caller must see it).
	join, ok := s.resolveProject(w, r, byName(p.Project))
	if !ok {
		return
	}
	// A namespace takes a primary network only when created: labeled for a
	// network its project lacks, it could never run a pod.
	if p.ProjectNetwork && !s.projectNetwork(join.proj) {
		fail(w, invalid(fmt.Errorf("project %s has no VM Network to join", join.proj.Name)))
		return
	}
	plat, ok := s.platformScopeGated(w, r)
	if !ok {
		return
	}
	claims := s.claimNamespaces(r.Context(), plat, join.proj.Name)
	if isDryRun(r) {
		p, err := s.draft.PreviewCreateNamespace(join.id, plat.proj, join.proj, raw, claims)
		respond(w, p, err)
		return
	}
	view, err := s.draft.StageCreateNamespace(join.id, plat.proj, join.proj, raw, claims)
	respond(w, view, err)
}

// handleCreateProject bootstraps a new tenant project from the UI - the "New
// Project" flow. It creates the project's forge repo and stages its first namespace
// (+ an optional owners RoleBinding) into the platform repo, gated on the claim
// on that namespace (see claimNamespaces), which the coordinator reads before the
// repo exists.
func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	// Peek the name for an early 400; the Coordinator re-decodes the full spec.
	raw, p, ok := readBody[struct {
		Name           string    `json:"name"`
		VMNetwork      *struct{} `json:"vmNetwork"`
		ProjectNetwork *struct {
			Name string `json:"name"`
		} `json:"projectNetwork"`
	}](w, r, false)
	if !ok {
		return
	}
	if p.Name == "" {
		fail(w, invalid(errors.New("a project name is required")))
		return
	}
	if p.VMNetwork != nil && !s.gateServed(w, r, model.KindUDN) {
		return
	}
	if p.ProjectNetwork != nil {
		if !s.gateServed(w, r, model.KindCUDN) {
			return
		}
		// Argo would take over a live network by that name, moving its
		// namespaces onto this project's.
		if s.netstate != nil && s.netstate.Holds(model.KindCUDN.Group, model.KindCUDN.Plural, "", p.ProjectNetwork.Name) {
			fail(w, fmt.Errorf("%w: a network named %s exists", model.ErrConflict, p.ProjectNetwork.Name))
			return
		}
	}
	plat, ok := s.platformScopeGated(w, r)
	if !ok {
		return
	}
	// The cluster is dotvirt's registry, so an existing project is what makes this a
	// re-manage rather than a create. The coordinator asks once its gate passed and
	// before it touches the forge: a refusal never leaves a repo behind, and never
	// tells a caller without the rights which projects exist.
	exists := func(name string) bool {
		_, ok := s.projectByName(name)
		return ok
	}
	claims := s.claimNamespaces(r.Context(), plat, p.Name)
	if isDryRun(r) {
		p, err := s.draft.PreviewCreateProject(plat.id, plat.proj, raw, claims, exists)
		respond(w, p, err)
		return
	}
	view, err := s.draft.StageCreateProject(plat.id, plat.proj, raw, claims, exists)
	respond(w, view, err)
}

// handleAdoptProject wires a repo to an existing labeled-but-repoless project - the
// "Attach repo" action on the inventory's no-repo dead-end. It lands a Namespace +
// repo annotation per namespace in the platform tier, each gated on the claim on
// that namespace; the target tenant (tenancyTarget) must currently be
// repoless.
func (s *Server) handleAdoptProject(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeOptional[struct {
		Owners []string `json:"owners,omitempty"`
	}](w, r)
	if !ok {
		return
	}
	plat, ok := s.platformScopeGated(w, r)
	if !ok {
		return
	}
	target, ok := s.tenancyTarget(w, r, plat)
	if !ok {
		return
	}
	view, err := s.draft.AdoptProject(plat.id, plat.proj, target, body.Owners, s.claimNamespaces(r.Context(), plat, target.Name))
	respond(w, view, err)
}

// handleProjectSyncAccess stages the platform-repo grants a project made before
// sync impersonation needs: each of its namespaces' admin RoleBinding gains
// the project's Argo sync account, beside that account's EgressFirewall grant.
// It lands only RoleBindings, which the coordinator weighs as the caller: a
// route gate on namespaces would refuse what RBAC allows.
func (s *Server) handleProjectSyncAccess(w http.ResponseWriter, r *http.Request) {
	plat, ok := s.platformScopeGated(w, r)
	if !ok {
		return
	}
	target, ok := s.tenancyTarget(w, r, plat)
	if !ok {
		return
	}
	view, err := s.draft.StageSyncAccess(plat.id, plat.proj, target)
	respond(w, view, err)
}

// handlePublishing stages what a project's publishing lacks: its Gateway,
// its certificate and the HTTPRoute grant of each namespace it reaches land in
// the platform repo, each weighed by the coordinator as the caller.
func (s *Server) handlePublishing(w http.ResponseWriter, r *http.Request) {
	if !s.gateServed(w, r, s.publishingKinds()...) {
		return
	}
	plat, ok := s.platformScopeGated(w, r)
	if !ok {
		return
	}
	target, ok := s.tenancyTarget(w, r, plat)
	if !ok {
		return
	}
	view, err := s.draft.StagePublishing(plat.id, plat.proj, target, s.publishTarget(target))
	respond(w, view, err)
}

// handlePublishNamespace stages a project's publish namespace into the
// platform repo, for a project the cluster holds none for, gated on the
// claim of that namespace should one by its name exist.
func (s *Server) handlePublishNamespace(w http.ResponseWriter, r *http.Request) {
	plat, ok := s.platformScopeGated(w, r)
	if !ok {
		return
	}
	target, ok := s.tenancyTarget(w, r, plat)
	if !ok {
		return
	}
	view, err := s.draft.StagePublishNamespace(plat.id, plat.proj, target, s.publishTarget(target), s.claimNamespaces(r.Context(), plat, target.Name))
	respond(w, view, err)
}

// publishingKinds are the kinds publishing stages, each of which the cluster
// must serve: a Certificate only with an issuer.
func (s *Server) publishingKinds() []model.Kind {
	if s.cfg.Publish.Issuer != "" {
		return []model.Kind{model.KindGateway, model.KindCert}
	}
	return []model.Kind{model.KindGateway}
}

// publishTarget is the project's namespaces, and its publish namespace, as
// the snapshots see them.
func (s *Server) publishTarget(p project.ProjectInfo) changeset.PublishTarget {
	pt := changeset.PublishTarget{
		Networks:      map[string]string{},
		RoutesGranted: s.state != nil && s.state.AdminWritesRoutes(),
		Isolated:      s.gateways != nil && s.gateways.GatewaysIsolated(s.cfg.Publish.GatewayClass),
	}
	pt.ProjectNetwork = s.projectNetwork(p)
	namespaces := p.Namespaces
	if s.state != nil {
		if ns, ok := s.state.PublishNamespace(p.Name); ok {
			pt.PublishNamespace = true
			namespaces = append(slices.Clone(namespaces), ns)
		}
		pt.PublishNameHeld, pt.PublishNameRequested = s.state.PublishNameHeld(p.Name)
	}
	for _, ns := range namespaces {
		pt.Networks[ns] = changeset.DefaultNetwork
		if s.netstate != nil {
			pt.Networks[ns] = s.netstate.PrimaryNetwork(ns)
		}
	}
	return pt
}

// handleReleaseProject dissolves a repoless project back into a plain
// namespace (or an adoptable tenant): declared tenancy is staged as a
// platform-repo rewrite (the PR is the release), label residue is stripped
// imperatively under the caller's token: the coordinator checks the first, the
// apiserver the second. Repo-backed projects are refused - release exists for
// the "no repo configured" dead end, not for tearing down healthy tenants.
func (s *Server) handleReleaseProject(w http.ResponseWriter, r *http.Request) {
	plat, ok := s.platformScopeGated(w, r)
	if !ok {
		return
	}
	target, ok := s.tenancyTarget(w, r, plat)
	if !ok {
		return
	}
	if target.Repo != "" {
		fail(w, fmt.Errorf("%w: project %q has a repo configured; release covers only the repoless dead end", model.ErrConflict, target.Name))
		return
	}
	staged, residue, err := s.draft.ReleaseDeclared(plat.id, plat.proj, target)
	if err != nil {
		fail(w, err)
		return
	}
	res := model.ReleaseResult{Staged: staged}
	for _, ns := range residue {
		// The k8s API answers too: anything already staged stays in the draft
		// (visible, unstageable).
		if perr := plat.cluster.ReleaseNamespace(r.Context(), ns, s.cfg.ProjectLabel, s.cfg.RepoAnnotation); perr != nil {
			fail(w, fmt.Errorf("release %s: %w", ns, perr))
			return
		}
		res.Released = append(res.Released, ns)
	}
	if len(staged) > 0 {
		if view, verr := s.draft.Get(plat.id, plat.proj); verr == nil {
			res.Draft = &view
		}
	}
	respond(w, res, nil)
}

// projectNetwork reports whether p has a VM Network its new namespaces may
// join: one of its namespaces carries tenancy.NetworkLabel for it, which only
// the platform writes, and only when it creates the namespace on it.
func (s *Server) projectNetwork(p project.ProjectInfo) bool {
	if s.state == nil {
		return false
	}
	return slices.ContainsFunc(p.Namespaces, func(ns string) bool {
		return s.state.NamespaceLabels(ns)[tenancy.NetworkLabel] == p.Name
	})
}
