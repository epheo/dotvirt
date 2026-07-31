package api

import (
	"net/http"

	"github.com/epheo/dotvirt/internal/model"
)

// The HA routes are platform-tier: node remediation fences any worker, so its
// config commits to the platform repo, and the mutating routes gate on the
// caller's authority to create the NodeHealthCheck CR - matching the
// AppProject boundary that lets only the platform app apply it.

// handleHA reports the HA tier: the platform repo's committed configuration,
// the caller's staged draft, the live operator state from the SA-watched
// snapshot, and the caller's authoring capability. Snapshot + git-mirror reads
// only - the SSAR rides the per-token cache (the panel polls this endpoint).
// Each plane degrades independently: a git-side failure becomes a Warning on
// an otherwise-served view, never a 500 that hides the live state.
func (s *Server) handleHA(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return
	}
	var view model.HAView
	if s.nodehealth != nil {
		view.Live = s.nodehealth.Live()
	}
	if s.cfg.PlatformRepo != "" && s.draft != nil {
		view.CanManage = s.canCreateCached(r.Context(), id, c, ssarNodeHealthCheck)
		platform := s.platformProject()
		if git, err := s.draft.HAState(platform); err != nil {
			view.Warning = "platform repo unavailable — committed HA state unknown: " + err.Error()
		} else {
			view.Configured, view.Config = git.Configured, git.Config
		}
		if view.CanManage {
			if d, err := s.draft.HADraft(id, platform); err == nil && (d.Config != nil || d.DisableStaged) {
				view.Draft = &d
			}
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// handleHAEnable stages the HA file set - operator install + NodeHealthCheck
// CR - into the platform draft.
func (s *Server) handleHAEnable(w http.ResponseWriter, r *http.Request) {
	raw, _, ok := peek[struct{}](w, r)
	if !ok {
		return
	}
	sc, ok := s.platformScope(w, r, ssarNodeHealthCheck)
	if !ok {
		return
	}
	view, err := s.draft.StageEnableHA(sc.id, sc.proj, raw)
	respond(w, view, err)
}

// handleHADisable stages the removal of the NodeHealthCheck CR (the operator
// install stays committed).
func (s *Server) handleHADisable(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.platformScope(w, r, ssarNodeHealthCheck)
	if !ok {
		return
	}
	view, err := s.draft.StageDisableHA(sc.id, sc.proj)
	respond(w, view, err)
}
