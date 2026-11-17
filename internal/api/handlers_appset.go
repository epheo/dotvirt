package api

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// handleAppSetPlugin serves the ArgoCD ApplicationSet plugin generator: it returns
// one {project,repo,path,namespace} parameter per repo-backed project (1 app = 1 project =
// its N labeled namespaces), derived from the dotvirt.io/project labels (read from the
// SA snapshot). Every emitted app lands in its project's own AppProject, which the
// operator derives from the same labels and confines to those namespaces and that
// repo under the same pkg/tenancy rules, so this list can only ever mint RESTRICTED
// tenant apps (namespaced workloads, no cluster-scoped infra, no other tenant's
// namespace) - the privileged platform app
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
	// The operator names the platform namespaces in the generator's input, so the
	// destination is chosen against the same reserved set its AppProjects use.
	req, ok := decodeOptional[pluginRequest](w, r)
	if !ok {
		return
	}
	platform := req.Input.Parameters.ReservedNamespaces
	type param struct {
		Project   string `json:"project"`
		Repo      string `json:"repo"`
		Path      string `json:"path"`
		Namespace string `json:"namespace"`
		// AllowEmpty is the app's automated.allowEmpty (the operator's
		// templatePatch): on only while a deliberate removal of the project's
		// last objects is held, so Argo's guard against an emptied source
		// still holds a repo that was lost and re-created.
		AllowEmpty bool `json:"allowEmpty"`
	}
	all := s.state.Namespaces()
	created := make(map[string]time.Time, len(all))
	for _, ns := range all {
		created[ns.Name] = ns.Created
	}
	params := []param{}
	for _, p := range s.resolver.Resolve(all, nil) {
		if p.Repo == "" {
			continue // a project with no usable repo can't be synced
		}
		// No AppProject can confine it, or none of its namespaces is admitted:
		// an Application would fail, so none is generated.
		if _, ok := tenancy.AppProjectName(p.Name); !ok {
			continue
		}
		nss := make([]tenancy.Namespace, 0, len(p.Namespaces))
		for _, ns := range p.Namespaces {
			nss = append(nss, tenancy.Namespace{Name: ns, Created: created[ns]})
		}
		dest, ok := tenancy.Destination(p.Name, nss, platform)
		if !ok {
			continue
		}
		root := p.Path
		if root == "" {
			root = "."
		}
		params = append(params, param{Project: p.Name, Repo: p.Repo, Path: root, Namespace: dest, AllowEmpty: s.EmptyHold(p) == model.HoldDeleting})
	}
	writeJSON(w, map[string]any{"output": map[string]any{"parameters": params}})
}

// EmptyHold is what it means that ArgoCD holds p's app at an empty source
// (argo.Snapshot.EmptyHold), "" while it does not; changeset.HoldReader. Deleting needs the revision
// Argo holds on main in dotvirt's mirror, descending from the one Argo last
// applied; until the mirror has it, pending. A held project's mirror is kept
// open (RepoSet.Warm): the merge that emptied it may land long after anyone
// last opened the repo.
func (s *Server) EmptyHold(p project.ProjectInfo) model.Hold {
	if s.drift == nil || p.Repo == "" {
		return ""
	}
	h, ok := s.drift.EmptyHold(p.Repo, p.Path)
	if !ok {
		return ""
	}
	read, ok := s.repos.Warm(p.Repo, p.Path)
	if !ok {
		return model.HoldPending
	}
	follows, known, err := read.Follows(s.cfg.BaseBranch, h.Compared, h.Applied)
	switch {
	case err != nil || !known:
		return model.HoldPending
	case follows:
		return model.HoldDeleting
	}
	return model.HoldReplaced
}

// pluginRequest is the plugin generator's request: the ApplicationSet's
// generator input, as the operator renders it.
type pluginRequest struct {
	Input struct {
		Parameters struct {
			ReservedNamespaces []string `json:"reservedNamespaces"`
		} `json:"parameters"`
	} `json:"input"`
}
