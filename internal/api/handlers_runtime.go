package api

import (
	"context"
	"net/http"

	"github.com/epheo/dotvirt/internal/cluster"
)

type runtimeOp func(ctx context.Context, c *cluster.Client, namespace, name string) error

// handleRuntimeOp runs an imperative VMI action under the caller's token - the
// subresource's own RBAC is the sole gate (no SA escalation, unlike resync).
// These don't mutate the git-managed spec, so ArgoCD self-heal won't revert them.
// verb labels the act in the shared Recent Tasks feed.
func (s *Server) handleRuntimeOp(w http.ResponseWriter, r *http.Request, verb string, op runtimeOp) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	err := op(r.Context(), sc.cluster, ns, name)
	s.recordTask(verb, ns, name, sc.id.Username, err == nil)
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	s.handleRuntimeOp(w, r, "Restart", func(ctx context.Context, c *cluster.Client, ns, name string) error { return c.Restart(ctx, ns, name) })
}

// handleMigrate accepts an optional target: {"node": "..."} pins the migration
// to that host; an empty body (or empty node) leaves placement to the scheduler.
func (s *Server) handleMigrate(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeOptional[struct {
		Node string `json:"node"`
	}](w, r)
	if !ok {
		return
	}
	s.handleRuntimeOp(w, r, "Live-migration", func(ctx context.Context, c *cluster.Client, ns, name string) error {
		return c.Migrate(ctx, ns, name, req.Node)
	})
}
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.handleRuntimeOp(w, r, "Pause", func(ctx context.Context, c *cluster.Client, ns, name string) error { return c.Pause(ctx, ns, name) })
}
func (s *Server) handleUnpause(w http.ResponseWriter, r *http.Request) {
	s.handleRuntimeOp(w, r, "Unpause", func(ctx context.Context, c *cluster.Client, ns, name string) error { return c.Unpause(ctx, ns, name) })
}
