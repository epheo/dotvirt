package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// handleAppSetPlugin serves the ArgoCD ApplicationSet plugin generator: it returns
// one {project,repo,namespace} parameter per repo-backed project namespace, derived
// from the dotvirt.io/project labels (read from the SA snapshot). Labeling a
// namespace thus makes the platform ApplicationSet provision its Argo Application
// automatically — dotvirt supplies the list but still never creates the App, so the
// "owns nothing" contract holds. Authenticated by a shared token (not a user
// session); the path is exempted from the user-auth middleware in auth.isOpenPath.
func (s *Server) handleAppSetPlugin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AppSetPluginToken == "" {
		http.Error(w, "appset plugin not configured", http.StatusNotFound)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
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
	for _, p := range s.resolver.Resolve(s.state.Namespaces(), nil) {
		if p.Repo == "" {
			continue // a project with no usable repo can't be synced
		}
		for _, ns := range p.Namespaces {
			params = append(params, param{Project: p.Name, Repo: p.Repo, Namespace: ns})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"output": map[string]any{"parameters": params}})
}
