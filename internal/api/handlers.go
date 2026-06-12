package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/inventory"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/restfactory"
)

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

// InventoryForIdentity builds the multi-tenant inventory visible to id: resolve
// the user's projects, gather their live + drift view under the user's token, and
// assemble. Exported so the WebSocket hub (per subscriber) reuses the exact same
// path as GET /api/inventory.
func (s *Server) InventoryForIdentity(ctx context.Context, id auth.Identity) (model.Inventory, error) {
	if s.clusterF == nil {
		// The WS hub calls this directly (bypassing userCluster's guard); fail closed
		// rather than nil-deref the factory and crash the hub goroutine.
		return model.Inventory{}, fmt.Errorf("cluster not configured")
	}
	userCluster, err := s.clusterF.For(id.Token)
	if err != nil {
		return model.Inventory{}, err
	}
	projects, err := s.projectsFor(ctx, id, userCluster)
	if err != nil {
		return model.Inventory{}, err
	}

	// Live state + topology come from the SA-owned snapshot (in-memory, no cluster
	// call), so a broadcast to N subscribers no longer issues N×(LIST+GET) — the
	// throttling that wedged the server is gone. enrich() applies live/drift only to
	// VMs already in the user's resolved projects, so the shared snapshot leaks
	// nothing across tenants.
	in := inventory.Inputs{
		Projects: projects,
		Branch:   s.cfg.BaseBranch,
		Repos:    s.repos,
		Live:     s.state.LiveVMs(),
	}
	// Drift is TTL-cached + shared across subscribers. A nil cache means Argo is
	// intentionally off (no warning); a non-nil cache that errors is a degradation
	// worth surfacing.
	var warnings []string
	if s.drift != nil {
		if drift, err := s.drift.Get(ctx); err == nil {
			in.Drift = drift // non-nil (VMDrift always returns a map) ⇒ drift enabled
		} else {
			warnings = append(warnings, "sync status is temporarily unavailable")
		}
	}
	inv := inventory.Build(in)
	inv.Warnings = warnings
	inv.Proposals = s.proposalsFor(id, projects)
	return inv, nil
}

// proposalsFor returns id's open PRs across its projects, cached per token for
// proposalsTTL. It rides the inventory broadcast so the UI's open-PR lane updates
// live (a merge moves main → the git poll rebroadcasts → the lane repaints), with
// the TTL sparing the forge a FindPR-per-project on every frame. Best-effort: a
// project whose forge lookup errors is skipped, never failing the inventory.
func (s *Server) proposalsFor(id auth.Identity, projects []project.ProjectInfo) []model.Proposal {
	if s.draft == nil {
		return nil
	}
	if v, ok := s.proposals.Get(restfactory.TokenKey(id.Token)); ok {
		return v
	}
	out := []model.Proposal{}
	for _, p := range projects {
		pr, ok, err := s.draft.OpenProposal(id, p)
		if err != nil {
			log.Printf("proposals: %s: %v (skipping)", p.Name, err)
			continue
		}
		if ok {
			out = append(out, pr)
		}
	}
	s.proposals.Put(restfactory.TokenKey(id.Token), out)
	return out
}

func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	id, _, err := s.userCluster(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	inv, err := s.InventoryForIdentity(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// handleOptions lists the wizard/editor choices (instancetypes, preferences, OS
// images, networks). These are cluster catalog data, the same for every tenant, so
// they're read with dotvirt's SA — a scoped tenant usually lacks cluster-scoped
// list on these CRDs, which would otherwise yield silently-empty dropdowns. The
// caller must still be authenticated (middleware) to reach here.
func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.FromContext(r.Context()); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	// Catalog is identical for everyone; serve the shared cache to skip 4 cluster
	// LISTs per wizard open.
	if v, ok := s.options.Get("all"); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}
	sa, err := s.clusterF.SA()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	opts, err := sa.ListOptions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.options.Put("all", opts)
	writeJSON(w, http.StatusOK, opts)
}

// handleAppSetPlugin serves the ArgoCD ApplicationSet plugin generator: it returns
// one {project,repo,namespace} parameter per repo-backed project namespace, derived
// from the dotvirt.io/project labels (read from the SA snapshot). Labeling a
// namespace thus makes the platform ApplicationSet provision its Argo Application
// automatically — dotvirt supplies the list but still never creates the App, so the
// "owns nothing" contract holds. Authenticated by a shared token (not a user
// session); the path is exempted from the user-auth middleware in auth.isOpenPath.
func (s *Server) handleAppSetPlugin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AppSetPluginToken == "" {
		http.Error(w, "appset plugin not configured", http.StatusNotFound)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.AppSetPluginToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	type param struct {
		Project   string `json:"project"`
		Repo      string `json:"repo"`
		Namespace string `json:"namespace"`
	}
	params := []param{}
	for _, p := range s.resolver.Resolve(s.state.Namespaces(), nil) {
		if p.Repo == "" {
			continue // a project with no usable repo can't be synced
		}
		for _, ns := range p.Namespaces {
			params = append(params, param{Project: p.Name, Repo: p.Repo, Namespace: ns})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"output": map[string]any{"parameters": params}})
}

// handleAllEvents lists recent VM/VMI Events across the caller's visible
// namespaces — the dock's Events lane. Read under the caller's token, scoped to
// the namespaces they may see.
func (s *Server) handleAllEvents(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// Scope to the repo-backed projects' namespaces (the managed inventory), not
	// every visible namespace — listing events across an admin's whole cluster takes
	// many seconds and matches no VM the UI shows.
	projects, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var nss []string
	for _, p := range projects {
		if p.Repo == "" {
			continue
		}
		nss = append(nss, p.Namespaces...)
	}
	events, err := c.ListVMEvents(r.Context(), nss)
	respond(w, events, err)
}

// handleHistory lists recent commits on the project's base branch — the Changes
// pane's history view.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byName(r.PathValue("project")))
	if !ok {
		return
	}
	if sc.proj.Repo == "" {
		writeJSON(w, http.StatusOK, []model.Commit{})
		return
	}
	read, _, err := s.repos.Get(sc.proj.Repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	commits, err := read.History(s.cfg.BaseBranch, 25)
	respond(w, commits, err)
}

// handleRevert proposes a forward commit reverting one commit in the project's
// repo — a new PR, never a history rewrite.
func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byName(r.PathValue("project")))
	if !ok {
		return
	}
	var req struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Hash == "" {
		http.Error(w, "commit hash is required", http.StatusBadRequest)
		return
	}
	result, err := s.draft.Revert(sc.id, sc.proj, req.Hash)
	respond(w, result, err)
}

// handleMetrics returns a VM's performance time-series (the Performance tab),
// queried from the configured Prometheus/Thanos endpoint under the caller's token —
// so the metrics backend's own RBAC gates which namespaces' data is returned.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	sc, ok := s.resolveProject(w, r, byNamespace(ns))
	if !ok {
		return
	}
	m, err := s.metrics.VMMetrics(r.Context(), sc.id.Token, ns, name, r.URL.Query().Get("range"))
	respond(w, m, err)
}

// handleVMUsage returns a VM's point-in-time capacity-and-usage (the Summary tab's
// "Capacity and Usage" bars), queried under the caller's token.
func (s *Server) handleVMUsage(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	sc, ok := s.resolveProject(w, r, byNamespace(ns))
	if !ok {
		return
	}
	u, err := s.metrics.VMUsage(r.Context(), sc.id.Token, ns, name)
	respond(w, u, err)
}

// handleClusterSummary returns the aggregate capacity view (the "All VMs" cluster
// landing): rings of VM usage vs node-allocatable capacity, VM counts by phase, and
// top-consumer VMs. VM-scoped sums are limited to the caller's visible namespaces.
func (s *Server) handleClusterSummary(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}
	id, c, err := s.userCluster(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	projects, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Aggregate over the repo-backed projects' namespaces — the same VMs the
	// inventory grid shows. Optionally narrow to one project / namespace / node so
	// every container level (all, project, namespace, node) gets its own summary.
	wantProject := r.URL.Query().Get("project")
	wantNamespace := r.URL.Query().Get("namespace")
	node := r.URL.Query().Get("node")
	var nss []string
	for _, p := range projects {
		if p.Repo == "" || (wantProject != "" && p.Name != wantProject) {
			continue
		}
		for _, n := range p.Namespaces {
			if wantNamespace != "" && n != wantNamespace {
				continue
			}
			nss = append(nss, n)
		}
	}
	cs, err := s.metrics.ClusterSummary(r.Context(), id.Token, nss, node)
	respond(w, cs, err)
}

// handleProposals lists the caller's open PRs across their visible projects — the
// same set the live inventory now carries; kept as a standalone read for parity.
func (s *Server) handleProposals(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	projects, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := s.proposalsFor(id, projects)
	if out == nil {
		out = []model.Proposal{}
	}
	writeJSON(w, http.StatusOK, out)
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

func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	var req model.EditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.SourceFile == "" {
		http.Error(w, "sourceFile is required", http.StatusBadRequest)
		return
	}
	result, err := s.draft.StageEdit(sc.id, sc.proj, r.PathValue("namespace"), r.PathValue("name"), req)
	respond(w, result, err)
}

// handleCreate stages a new VM. The path carries no namespace, so we peek the
// spec's namespace to pick the target project.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var peek struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil || peek.Namespace == "" {
		http.Error(w, "spec namespace is required", http.StatusBadRequest)
		return
	}
	sc, ok := s.resolveProject(w, r, byNamespace(peek.Namespace))
	if !ok {
		return
	}
	result, err := s.draft.StageCreate(sc.id, sc.proj, raw)
	respond(w, result, err)
}

func (s *Server) handleDraftGet(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.draftScope(w, r)
	if !ok {
		return
	}
	view, err := s.draft.Get(sc.id, sc.proj)
	respond(w, view, err)
}

func (s *Server) handleDraftDiscard(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.draftScope(w, r)
	if !ok {
		return
	}
	if err := s.draft.Discard(sc.id, sc.proj); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUnstage(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	if err := s.draft.Unstage(sc.id, sc.proj, r.PathValue("namespace"), r.PathValue("name")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.draftScope(w, r)
	if !ok {
		return
	}
	var req model.ProposeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.draft.Propose(sc.id, sc.proj, req)
	respond(w, result, err)
}

func (s *Server) handleDrift(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	result, err := s.draft.VMDrift(sc.proj, r.PathValue("namespace"), r.PathValue("name"))
	respond(w, result, err)
}

// handleEvents lists recent Kubernetes Events for a VM (+ its VMI) — the Monitor
// tab. Read under the caller's token; resolveProject gates the namespace.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	events, err := sc.cluster.ListEvents(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	respond(w, events, err)
}

func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	result, err := s.draft.Adopt(sc.id, sc.proj, r.PathValue("namespace"), r.PathValue("name"))
	respond(w, result, err)
}

// handleDelete stages the removal of a VM's manifest into the caller's draft. Like
// edit/adopt it only mutates the user's own draft (no cluster write, no SA
// escalation — Argo prunes the VM on merge under its own RBAC), so namespace
// membership via resolveProject is the right gate, not resync's CanUpdateVM check.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	result, err := s.draft.StageDelete(sc.id, sc.proj, r.PathValue("namespace"), r.PathValue("name"))
	respond(w, result, err)
}

func (s *Server) handleResync(w http.ResponseWriter, r *http.Request) {
	// Resync runs the reconcile with dotvirt's SA, so gate it on the caller's OWN
	// authority over the VM (not just namespace read): they may trigger a sync only
	// if they could update the VM themselves — otherwise read access would escalate
	// into an SA-privileged Argo sync.
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if allowed, err := sc.cluster.CanUpdateVM(r.Context(), ns, name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !allowed {
		http.Error(w, "you don't have permission to sync this VM", http.StatusForbidden)
		return
	}
	result, err := s.draft.Resync(ns, name)
	respond(w, result, err)
}

// runtimeOp is one imperative VMI action (restart/migrate/pause/unpause).
type runtimeOp func(ctx context.Context, c *cluster.Client, namespace, name string) error

// handleRuntimeOp runs an imperative VMI action under the caller's token — the
// subresource's own RBAC is the sole gate (no SA escalation, unlike resync).
// These don't mutate the git-managed spec, so ArgoCD self-heal won't revert them.
func (s *Server) handleRuntimeOp(w http.ResponseWriter, r *http.Request, op runtimeOp) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	if err := op(r.Context(), sc.cluster, r.PathValue("namespace"), r.PathValue("name")); err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runtimeOpStatus maps a KubeVirt subresource error to an HTTP status: Forbidden
// (RBAC) → 403, NotFound → 404, Conflict/BadRequest (e.g. pause a stopped VM,
// migrate a non-migratable one) → 409.
func runtimeOpStatus(err error) int {
	switch {
	case apierrors.IsForbidden(err):
		return http.StatusForbidden
	case apierrors.IsNotFound(err):
		return http.StatusNotFound
	case apierrors.IsConflict(err), apierrors.IsBadRequest(err):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	s.handleRuntimeOp(w, r, func(ctx context.Context, c *cluster.Client, ns, name string) error { return c.Restart(ctx, ns, name) })
}
func (s *Server) handleMigrate(w http.ResponseWriter, r *http.Request) {
	s.handleRuntimeOp(w, r, func(ctx context.Context, c *cluster.Client, ns, name string) error { return c.Migrate(ctx, ns, name) })
}
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.handleRuntimeOp(w, r, func(ctx context.Context, c *cluster.Client, ns, name string) error { return c.Pause(ctx, ns, name) })
}
func (s *Server) handleUnpause(w http.ResponseWriter, r *http.Request) {
	s.handleRuntimeOp(w, r, func(ctx context.Context, c *cluster.Client, ns, name string) error { return c.Unpause(ctx, ns, name) })
}

// --- snapshots (the Snapshots tab) — imperative + RBAC-gated like the runtime ops,
// not git-managed, so ArgoCD never reverts them.

func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	snaps, err := sc.cluster.ListSnapshots(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	respond(w, snaps, err)
}

// handleTakeSnapshot creates a point-in-time snapshot; a name may be supplied, else
// one is derived from the VM name + timestamp.
func (s *Server) handleTakeSnapshot(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	snapName := req.Name
	if snapName == "" {
		snapName = name + "-" + time.Now().UTC().Format("20060102-150405")
	}
	if err := sc.cluster.CreateSnapshot(r.Context(), ns, name, snapName); err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": snapName})
}

// handleRestoreSnapshot rolls the VM back to a snapshot (the VM must be stopped).
func (s *Server) handleRestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	if err := sc.cluster.RestoreSnapshot(r.Context(), r.PathValue("namespace"), r.PathValue("name"), r.PathValue("snapshot")); err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	if err := sc.cluster.DeleteSnapshot(r.Context(), r.PathValue("namespace"), r.PathValue("snapshot")); err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
