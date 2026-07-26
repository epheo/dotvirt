package api

import (
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// handleFinishSSO registers the OAuthClient under the CALLER's token: the API
// server's RBAC is the entire gate, so no dotvirt identity holds oauthclients
// permissions and a non-admin gets a plain 403. One click replaces the
// status.ssoOAuthClient copy-paste.
func (s *Server) handleFinishSSO(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.Error(w, "OpenShift SSO is not enabled on this install", http.StatusNotFound)
		return
	}
	_, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return
	}
	id, secret, redirect := s.oauth.DesiredClient()
	if err := c.ApplyOAuthClient(r.Context(), id, secret, redirect); err != nil {
		if apierrors.IsForbidden(err) {
			http.Error(w, "your token may not manage OAuthClients; a cluster admin has to finish SSO", http.StatusForbidden)
			return
		}
		fail(w, err)
		return
	}
	s.oauth.MarkRegistered()
	w.WriteHeader(http.StatusNoContent)
}
