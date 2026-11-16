package api

import (
	"net/http"

	"github.com/epheo/dotvirt/internal/auth"
)

// forgeSession is what GET /api/forge/session answers: whether pull requests
// go through the user's own forge account, and this session's connection.
type forgeSession struct {
	Delegated bool `json:"delegated"`
	auth.ForgeStatus
}

// handleForgeSession reports the caller's forge connection. With delegation
// off it says so, and the console offers no connect.
func (s *Server) handleForgeSession(w http.ResponseWriter, r *http.Request) {
	out := forgeSession{Delegated: s.delegated != nil}
	if id, ok := auth.FromContext(r.Context()); ok && s.delegated != nil {
		out.ForgeStatus = s.delegated.Status(id)
	}
	writeJSON(w, out)
}

// handleForgeDisconnect forgets the caller's forge tokens for this session.
func (s *Server) handleForgeDisconnect(w http.ResponseWriter, r *http.Request) {
	if id, ok := auth.FromContext(r.Context()); ok {
		s.delegated.Disconnect(id)
	}
	w.WriteHeader(http.StatusNoContent)
}
