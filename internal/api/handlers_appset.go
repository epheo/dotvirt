package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// handleAppSetPlugin serves the ArgoCD ApplicationSet plugin generator: it returns
// one {project,repo} parameter per repo-backed project (1 app = 1 project = its N
// labeled namespaces), derived from the dotvirt.io/project labels (read from the SA
// snapshot). Every emitted app lands in the dotvirt-tenants AppProject (the template
// in deploy/applicationset.yaml), so this list can only ever mint RESTRICTED tenant
// apps (namespaced workloads, no cluster-scoped infra) — the privileged platform app
// is static and platform-owned, never generated from here. Labeling a namespace
// folds it into its project's existing app — dotvirt supplies the list but never
// creates the App, so "owns nothing" holds. Authenticated by a shared token (not a
// user session); the path is exempted from user-auth in auth.isOpenPath.
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
		Project string `json:"project"`
		Repo    string `json:"repo"`
	}
	params := []param{}
	for _, p := range s.resolver.Resolve(s.state.Namespaces(), nil) {
		if p.Repo == "" {
			continue // a project with no usable repo can't be synced
		}
		params = append(params, param{Project: p.Name, Repo: p.Repo})
	}
	writeJSON(w, http.StatusOK, map[string]any{"output": map[string]any{"parameters": params}})
}
