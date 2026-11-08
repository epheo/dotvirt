package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
)

// revokeTimeout bounds the token delete a logout waits on.
const revokeTimeout = 10 * time.Second

// handleLogout ends the caller's session and closes its streams and consoles.
// An SSO session's token is also deleted cluster-wide, under the caller's own
// token: dotvirt's OAuth client minted it for this session alone. A failed
// delete is logged; the session has ended either way.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if !s.auth.Logout(w, r) {
		return
	}
	s.bus.Publish(eventbus.SessionEnded)
	if !id.SSO {
		return
	}
	name, ok := auth.OAuthTokenName(id.Token)
	if !ok {
		return
	}
	// Detached: a client hanging up after its logout must not keep the token alive.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), revokeTimeout)
	defer cancel()
	_, c, err := s.userCluster(r)
	if err == nil {
		err = c.DeleteOwnOAuthToken(ctx, name)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		log.Printf("api: %s: logout: token not revoked: %v", id.Username, err)
	}
}

// handleFinishSSO registers the OAuthClient under the CALLER's token: the API
// server's RBAC is the entire gate, so no dotvirt identity holds oauthclients
// permissions and a non-admin gets a plain 403. One click replaces the
// status.ssoOAuthClient copy-paste.
func (s *Server) handleFinishSSO(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		fail(w, fmt.Errorf("%w: OpenShift SSO is not enabled on this install", model.ErrNotFound))
		return
	}
	_, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	id, secret, redirect := s.oauth.DesiredClient()
	if err := c.ApplyOAuthClient(r.Context(), id, secret, redirect); err != nil {
		if apierrors.IsForbidden(err) {
			fail(w, fmt.Errorf("%w: your token may not manage OAuthClients; a cluster admin has to finish SSO", model.ErrForbidden))
			return
		}
		fail(w, err)
		return
	}
	s.oauth.MarkRegistered()
	w.WriteHeader(http.StatusNoContent)
}
