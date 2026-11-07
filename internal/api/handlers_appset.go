package api

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// handleAppSetPlugin serves the ArgoCD ApplicationSet plugin generator: it returns
// one {project,repo,namespace} parameter per repo-backed project (1 app = 1 project =
// its N labeled namespaces), derived from the dotvirt.io/project labels (read from the
// SA snapshot). Every emitted app lands in its project's own AppProject, which the
// operator derives from the same labels and confines to those namespaces and that
// repo, so this list can only ever mint RESTRICTED tenant apps (namespaced workloads,
// no cluster-scoped infra, no other tenant's namespace) - the privileged platform app
// is static and platform-owned, never generated from here. Labeling a namespace
// folds it into its project's existing app - dotvirt supplies the list but never
// creates the App, so "owns nothing" holds. Authenticated by a shared token (not a
// user session); the path is exempted from user-auth in auth.isOpenPath.
func (s *Server) handleAppSetPlugin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AppSetPluginToken == "" {
		fail(w, fmt.Errorf("%w: appset plugin not configured", model.ErrNotFound))
		return
	}
	// Require the Bearer scheme outright: TrimPrefix alone would also accept a
	// bare "Authorization: <token>", quietly widening the accepted forms.
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	got := strings.TrimPrefix(authz, "Bearer ")
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
	for _, p := range s.AllProjects() {
		if p.Repo == "" {
			continue // a project with no usable repo can't be synced
		}
		params = append(params, param{Project: p.Name, Repo: p.Repo, Namespace: destinationNamespace(p)})
	}
	writeJSON(w, map[string]any{"output": map[string]any{"parameters": params}})
}

// destinationNamespace is the Application's destination: where Argo puts a
// document without metadata.namespace. It must be one of the project's own
// namespaces, the only ones its AppProject admits. The namespace named after
// the project wins over name order so that adding a namespace never moves it:
// a move would recreate those documents' objects elsewhere.
func destinationNamespace(p project.ProjectInfo) string {
	if slices.Contains(p.Namespaces, p.Name) {
		return p.Name
	}
	return p.Namespaces[0]
}
