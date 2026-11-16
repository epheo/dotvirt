package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// revokeTimeout bounds the SSO token delete a logout runs.
const revokeTimeout = 10 * time.Second

// handleLogout ends the caller's session, answers, and closes the session's
// streams and consoles. What a sign-in flow minted for the session alone is
// then revoked: an OIDC session's refresh token at its issuer, an OpenShift
// SSO token cluster-wide, under that token itself. A failed revocation is
// logged; the session has ended either way. Only a session this call ended is
// revoked, so a replayed cookie costs no outbound call.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	id, ended := s.auth.Logout(w, r)
	if !ended {
		return
	}
	s.bus.Publish(eventbus.SessionEnded)
	name, ok := auth.OAuthTokenName(id.Token)
	oidc := id.Session != "" && s.oidc != nil
	if !oidc && (!id.SSO || !ok || s.clusterF == nil) {
		return
	}
	_ = http.NewResponseController(w).Flush() // the UI need not wait on the revocation
	// Detached: a client hanging up after its logout must not keep the token alive.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.revokeTimeout)
	defer cancel()
	if oidc {
		if err := s.oidc.Revoke(ctx, id); err != nil {
			log.Printf("api: logout: OIDC refresh token not revoked: %v", err)
		}
		return
	}
	c, err := s.clusterF.For(id.Token)
	if err == nil {
		err = c.DeleteOwnOAuthToken(ctx, name)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		log.Printf("api: logout: SSO token not revoked: %v", err)
	}
}

// handleFinishSSO registers the OAuthClient under the CALLER's token: the API
// server's RBAC is the entire gate, so no dotvirt identity holds oauthclients
// permissions and a non-admin gets a plain 403. One click replaces the
// status.ssoOAuthClient copy-paste. Its name and redirect come from what the
// caller reads (ssoClient), never from the env the operator writes; only the
// secret, which the operator generates and holds anyway, is the console's.
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
	id, redirect, err := s.ssoClient(r.Context(), c)
	if err != nil {
		fail(w, err)
		return
	}
	_, secret, _ := s.oauth.DesiredClient()
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

// ssoClient is the OAuthClient this console registers, as the caller reads
// it: named for the console's own namespace, redirecting to the install's
// spec.ingress.host, else the router's host for the console's Route.
func (s *Server) ssoClient(ctx context.Context, c *cluster.Client) (id, redirect string, err error) {
	ns := s.cfg.OwnNamespace
	if ns == "" {
		return "", "", fmt.Errorf("%w: the console does not know its namespace (not running in a pod)", model.ErrUnavailable)
	}
	readErr := func(what string, err error) error {
		if apierrors.IsForbidden(err) {
			return fmt.Errorf("%w: your token may not read %s; a cluster admin has to finish SSO", model.ErrForbidden, what)
		}
		return unavailable(fmt.Errorf("read %s: %w", what, err))
	}
	spec, err := c.InstallIn(ctx, ns)
	if err != nil {
		return "", "", readErr("the install", err)
	}
	domain := ""
	if spec.IngressHost == "" {
		if domain, err = c.AppsDomain(ctx); err != nil {
			return "", "", readErr("the cluster's ingress domain", err)
		}
	}
	return adminobjects.SSOClientName(ns), adminobjects.SSORedirect(spec.IngressHost, ns, domain), nil
}
