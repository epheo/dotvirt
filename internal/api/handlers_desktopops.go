package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/tasks"
)

// The admin acts on a desktop, each under the caller's token. Start, stop and
// release act on the claim holding a pool's desktop, never on the desktop's
// own spec.running: DeskuBe's policy refuses that on a pool's desktop, and git
// owns a declared one's. Restart deletes the desktop's pod. Each is asked
// here first, so a refusal names what the caller lacks; the apiserver and
// DeskuBe's admission then decide, and their answer is the caller's.

// claimAct is one act on the claim holding a pool's desktop.
type claimAct struct {
	verb  string // the tasks feed's, and the refusal's in lower case
	claim string // the verb on the claim
	run   func(ctx context.Context, c *cluster.Client, namespace, claim string) error
}

var (
	startDesktop = claimAct{"Start", verbPatch, func(ctx context.Context, c *cluster.Client, ns, claim string) error {
		return c.SetClaimRunning(ctx, ns, claim, true)
	}}
	stopDesktop = claimAct{"Stop", verbPatch, func(ctx context.Context, c *cluster.Client, ns, claim string) error {
		return c.SetClaimRunning(ctx, ns, claim, false)
	}}
	releaseDesktop = claimAct{"Release", verbDelete, func(ctx context.Context, c *cluster.Client, ns, claim string) error {
		return c.DeleteClaim(ctx, ns, claim)
	}}
)

func (s *Server) handleDesktopStart(w http.ResponseWriter, r *http.Request) {
	s.handleClaimAct(w, r, startDesktop)
}

func (s *Server) handleDesktopStop(w http.ResponseWriter, r *http.Request) {
	s.handleClaimAct(w, r, stopDesktop)
}

func (s *Server) handleDesktopRelease(w http.ResponseWriter, r *http.Request) {
	s.handleClaimAct(w, r, releaseDesktop)
}

// desktopScope resolves the desktop the path names for a caller who may list
// the desktops in its namespace, and the claims when claims is set, in a
// project with a repo: what GET /api/desktops would show them. Anything else
// is a 404, so no answer says what exists beyond that.
func (s *Server) desktopScope(w http.ResponseWriter, r *http.Request, claims bool) (scope, model.Desktop, model.DesktopClaim, bool) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	sc, ok := s.resolveProject(w, r, byNamespace(ns))
	if !ok {
		return sc, model.Desktop{}, model.DesktopClaim{}, false
	}
	v, err := s.viewFor(r.Context(), sc.id, sc.cluster)
	if err != nil {
		fail(w, err)
		return sc, model.Desktop{}, model.DesktopClaim{}, false
	}
	var d model.Desktop
	var holder model.DesktopClaim
	if sc.proj.Repo != "" && v.lists(ssarDesktops, ns) && (!claims || v.lists(ssarDesktopClaims, ns)) {
		d, holder, ok = s.desktops.Desktop(ns, name)
	} else {
		ok = false
	}
	if !ok {
		fail(w, fmt.Errorf("%w: desktop %s/%s", model.ErrNotFound, ns, name))
	}
	return sc, d, holder, ok
}

// handleClaimAct runs act on the claim the body names, which must still hold
// the pool's desktop the path names: a desktop handed to another person since
// the caller looked is refused, never acted on in their name.
func (s *Server) handleClaimAct(w http.ResponseWriter, r *http.Request, act claimAct) {
	sc, d, holder, ok := s.desktopScope(w, r, true)
	if !ok {
		return
	}
	req, ok := decode[struct {
		Claim string `json:"claim"`
	}](w, r)
	if !ok {
		return
	}
	switch {
	case req.Claim == "":
		fail(w, invalid(errors.New("name the claim holding the desktop")))
		return
	case d.Pool == "":
		fail(w, fmt.Errorf("%w: %s is made one by one: it is started and stopped in git", model.ErrConflict, d.Name))
		return
	case holder.Name != req.Claim || holder.Pool != d.Pool:
		fail(w, fmt.Errorf("%w: claim %s no longer holds %s: look at it again", model.ErrConflict, req.Claim, d.Name))
		return
	}
	if !s.gateClaimAct(w, r, sc, act, holder) {
		return
	}
	err := act.run(r.Context(), sc.cluster, d.Namespace, holder.Name)
	s.recordOp(tasks.Op{Verb: act.verb, Namespace: d.Namespace, Name: d.Name, Desktop: true, By: sc.id.Username, OK: err == nil})
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// gateClaimAct asks what DeskuBe's RBAC and admission ask of act on claim c:
// the verb on the claims, and of the pool what claimPoolVerbs names. ok=false
// means the refusal is written.
func (s *Server) gateClaimAct(w http.ResponseWriter, r *http.Request, sc scope, act claimAct, c model.DesktopClaim) bool {
	own := c.User == sc.id.Username
	ok, err := s.anyAllowed(r.Context(), sc.id, sc.cluster, claimActions(act.claim, c.Namespace, c.Pool, own))
	if err != nil {
		fail(w, s.reviewFailed(r.Context(), sc.id, err))
		return false
	}
	if ok {
		return true
	}
	whose := c.User + "'s desktop"
	if own {
		whose = "your desktop"
	}
	needs := fmt.Sprintf("%s vdiclaims in %s", act.claim, c.Namespace)
	if verbs := claimPoolVerbs(act.claim, own); len(verbs) > 0 {
		needs += fmt.Sprintf(", and %s on vdipool %s", strings.Join(verbs, " or "), c.Pool)
	}
	fail(w, fmt.Errorf("%w: you may not %s %s: that takes %s", model.ErrForbidden, strings.ToLower(act.verb), whose, needs))
	return false
}

// handleDesktopRestart deletes the pod of the desktop the path names: a new
// one comes, its home stays, and its session ends.
func (s *Server) handleDesktopRestart(w http.ResponseWriter, r *http.Request) {
	sc, d, _, ok := s.desktopScope(w, r, false)
	if !ok || !s.gate(w, r, sc, restartDesktopAction(d.Namespace)) {
		return
	}
	n, err := sc.cluster.RestartDesktop(r.Context(), d.Namespace, d.Name)
	if err == nil && n == 0 {
		err = fmt.Errorf("%w: %s runs no pod to restart: it is stopped, or still starting", model.ErrConflict, d.Name)
	}
	s.recordOp(tasks.Op{Verb: "Restart", Namespace: d.Namespace, Name: d.Name, Desktop: true, By: sc.id.Username, OK: err == nil})
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
