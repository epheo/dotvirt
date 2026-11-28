package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/desktops"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/tasks"
)

// The admin acts on a desktop, each under the caller's token. A pool's
// desktop starts, stops and is released on the claim holding it: never on the
// desktop's own spec.running, which DeskuBe's policy refuses on a pool's
// desktop. A desktop made one by one starts and stops on its owner's claim;
// it is not powered here yet. Any desktop restarts through DeskuBe's restart
// request: its pod and Deployment are DeskuBe's, never touched here. Each act
// is asked here first, so a refusal names what the caller lacks; the
// apiserver and DeskuBe's admission then decide, and their answer is the
// caller's.

// claimAct is one act on the claim holding a pool's desktop.
type claimAct struct {
	verb  string // the tasks feed's, and the refusal's in lower case
	claim string // the verb on the claim
	run   func(ctx context.Context, c *cluster.Client, namespace, claim, uid string) error
}

var (
	startDesktop = claimAct{"Start", verbPatch, func(ctx context.Context, c *cluster.Client, ns, claim, uid string) error {
		return c.SetClaimRunning(ctx, ns, claim, uid, true)
	}}
	stopDesktop = claimAct{"Stop", verbPatch, func(ctx context.Context, c *cluster.Client, ns, claim, uid string) error {
		return c.SetClaimRunning(ctx, ns, claim, uid, false)
	}}
	releaseDesktop = claimAct{"Release", verbDelete, func(ctx context.Context, c *cluster.Client, ns, claim, uid string) error {
		return c.DeleteClaim(ctx, ns, claim, uid)
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

// desktopScope resolves the desktop the path names, and the claim holding it,
// for a caller who may list the desktops and the claims in its namespace, in
// a project with a repo: what GET /api/desktops would show them. Anything
// else is a 404, so no answer says what exists beyond that.
func (s *Server) desktopScope(w http.ResponseWriter, r *http.Request) (scope, model.Desktop, model.DesktopClaim, bool) {
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
	if sc.proj.Repo != "" && v.lists(ssarDesktops, ns) {
		d, holder, ok = s.desktops.Desktop(ns, name)
	} else {
		ok = false
	}
	switch {
	case !ok:
		fail(w, fmt.Errorf("%w: desktop %s/%s", model.ErrNotFound, ns, name))
	case !v.lists(ssarDesktopClaims, ns):
		fail(w, fmt.Errorf("%w: desktop %s/%s: the claims there, which its acts read, are not listed to you", model.ErrNotFound, ns, name))
		ok = false
	}
	return sc, d, holder, ok
}

// claimRef is the claim an act names: the one the caller saw holding the
// desktop, by its name and UID.
type claimRef struct {
	Claim string `json:"claim"`
	UID   string `json:"uid"`
}

// handleClaimAct runs act on the claim the body names, which must still hold
// the pool's desktop the path names, and be the very claim the caller saw:
// a name is free once its claim is deleted, so a later claim under it may be
// another person's. The snapshot is trusted for that only while it is fresh;
// the write itself is held to the claim's UID, so a claim replaced since the
// snapshot last heard is refused by the apiserver.
func (s *Server) handleClaimAct(w http.ResponseWriter, r *http.Request, act claimAct) {
	sc, d, holder, ok := s.desktopScope(w, r)
	if !ok {
		return
	}
	req, ok := decode[claimRef](w, r)
	if !ok {
		return
	}
	switch {
	case req.Claim == "" || req.UID == "":
		fail(w, invalid(errors.New("name the claim holding the desktop, and its uid")))
		return
	case !s.desktops.Fresh(desktops.GVRInstance, desktops.GVRClaim):
		fail(w, fmt.Errorf("%w: the console's watch on the desktops or their claims is down, so who holds %s may have changed; acts wait until it is back", model.ErrUnavailable, d.Name))
		return
	case d.Pool == "":
		fail(w, fmt.Errorf("%w: %s starts and stops on its owner's claim; not acted on here yet", model.ErrConflict, d.Name))
		return
	case holder.Name != req.Claim || holder.UID != req.UID || d.ClaimUID != req.UID || holder.Pool != d.Pool:
		fail(w, fmt.Errorf("%w: claim %s no longer holds %s: look at it again", model.ErrConflict, req.Claim, d.Name))
		return
	}
	if !s.gateClaimAct(w, r, sc, act, holder) {
		return
	}
	err := act.run(r.Context(), sc.cluster, d.Namespace, holder.Name, holder.UID)
	s.recordOp(tasks.Op{Verb: act.verb, Namespace: d.Namespace, Name: d.Name, Desktop: true, By: sc.id.Username, OK: err == nil})
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// gateClaimAct asks what DeskuBe's RBAC and admission ask of act on claim c:
// the verb on the claim, and of the pool what claimPoolVerbs names. ok=false
// means the refusal is written.
func (s *Server) gateClaimAct(w http.ResponseWriter, r *http.Request, sc scope, act claimAct, c model.DesktopClaim) bool {
	own := c.User == sc.id.Username
	ok, err := s.anyAllowed(r.Context(), sc.id, sc.cluster, claimActions(act.claim, c.Namespace, c.Pool, c.Name, own))
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
	needs := fmt.Sprintf("%s vdiclaims %s in %s", act.claim, c.Name, c.Namespace)
	if verbs := claimPoolVerbs(act.claim, own); len(verbs) > 0 {
		needs += fmt.Sprintf(", and %s on vdipool %s", strings.Join(verbs, " or "), c.Pool)
	}
	fail(w, fmt.Errorf("%w: you may not %s %s: that takes %s", model.ErrForbidden, strings.ToLower(act.verb), whose, needs))
	return false
}

// desktopRef is the desktop an act names: the one the caller saw, by its UID.
type desktopRef struct {
	UID string `json:"uid"`
}

// handleDesktopRestart asks DeskuBe to restart the desktop the path names,
// which must be the very desktop the caller saw, and show a pod: a restart
// never starts a stopped desktop. The snapshot is trusted for its phase only
// while the desktops are fresh; the write itself is held to the desktop's
// UID.
func (s *Server) handleDesktopRestart(w http.ResponseWriter, r *http.Request) {
	sc, d, _, ok := s.desktopScope(w, r)
	if !ok {
		return
	}
	req, ok := decode[desktopRef](w, r)
	if !ok {
		return
	}
	switch {
	case req.UID == "":
		fail(w, invalid(errors.New("name the desktop's uid")))
		return
	case !s.desktops.Fresh(desktops.GVRInstance):
		fail(w, fmt.Errorf("%w: the console's watch on the desktops is down, so %s may have changed; acts wait until it is back", model.ErrUnavailable, d.Name))
		return
	case d.UID != req.UID:
		fail(w, fmt.Errorf("%w: desktop %s is not the one you saw: look at it again", model.ErrConflict, d.Name))
		return
	case !desktops.HasPod(d.Phase):
		fail(w, fmt.Errorf("%w: %s has no pod to restart: it is %s", model.ErrConflict, d.Name, cmp.Or(strings.ToLower(d.Phase), "not read by DeskuBe yet")))
		return
	}
	_, ok, err := s.allowed(r.Context(), sc.id, sc.cluster, desktopRestartAction(d.Namespace, d.Name))
	switch {
	case err != nil:
		fail(w, s.reviewFailed(r.Context(), sc.id, err))
		return
	case !ok:
		fail(w, fmt.Errorf("%w: you may not restart %s: that takes patch vdiinstances %s in %s", model.ErrForbidden, d.Name, d.Name, d.Namespace))
		return
	}
	err = sc.cluster.RestartDesktop(r.Context(), d.Namespace, d.Name, d.UID)
	s.recordOp(tasks.Op{Verb: "Restart", Namespace: d.Namespace, Name: d.Name, Desktop: true, By: sc.id.Username, OK: err == nil})
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
