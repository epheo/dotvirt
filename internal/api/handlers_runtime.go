package api

import (
	"context"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/epheo/dotvirt/internal/cluster"
)

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
