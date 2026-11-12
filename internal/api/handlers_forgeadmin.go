package api

import (
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/epheo/dotvirt/internal/model"
)

// handleForgeAdminPassword answers a managed forge's admin credential, read
// under the caller's token on their click: the apiserver is the check, and
// the answer is never stored, logged or broadcast.
func (s *Server) handleForgeAdminPassword(w http.ResponseWriter, r *http.Request) {
	a := s.cfg.ForgeAdmin
	_, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	password, err := c.SecretValue(r.Context(), a.Namespace, a.Name, a.Key)
	switch {
	case apierrors.IsForbidden(err):
		fail(w, fmt.Errorf("%w: you may not read the forge's admin password", model.ErrForbidden))
		return
	case apierrors.IsNotFound(err):
		fail(w, fmt.Errorf("%w: the forge's admin password is gone; the operator writes it again on its next pass", model.ErrNotFound))
		return
	case err != nil:
		fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, model.ForgeAdminCredential{Username: a.User, Password: password})
}
