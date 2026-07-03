package api

import (
	"encoding/json"
	"net/http"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// The DRS routes are platform-tier: the descheduler rebalances every node, so
// its config commits to the platform repo, and the mutating routes gate on the
// caller's authority to create the KubeDescheduler CR — matching the AppProject
// boundary that lets only the platform app apply it.

// handleDRS reports the DRS tier: the platform repo's committed configuration,
// the live operator state from the SA-watched snapshot, and the caller's
// authoring capability. Snapshot + git-mirror reads only — the two SSARs are
// the sole cluster calls.
func (s *Server) handleDRS(w http.ResponseWriter, r *http.Request) {
	_, c, err := s.userCluster(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	var view model.DRSView
	if s.desched != nil {
		view.Live = s.desched.Live()
	}
	if s.cfg.PlatformRepo != "" && s.draft != nil {
		ctx := r.Context()
		view.CanManage = c.CanCreateClusterResource(ctx, "operator.openshift.io", "kubedeschedulers")
		view.CanPSI = c.CanCreateClusterResource(ctx, "machineconfiguration.openshift.io", "machineconfigs")
		git, err := s.draft.DRSState(project.ProjectInfo{Name: platformProjectName, Repo: s.cfg.PlatformRepo})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		view.DRSGitState = git
	}
	writeJSON(w, http.StatusOK, view)
}

// handleDRSEnable stages the DRS (descheduler) file set — operator install +
// KubeDescheduler CR, optionally the PSI MachineConfig — into the platform
// draft. The PSI file reboots the worker pool when merged, so it carries its
// own machineconfigs-create SSAR on top of the kubedeschedulers gate.
func (s *Server) handleDRSEnable(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sc, ok := s.platformScope(w, r, "operator.openshift.io", "kubedeschedulers")
	if !ok {
		return
	}
	var peek struct {
		InstallPSI bool `json:"installPSI"`
	}
	_ = json.Unmarshal(raw, &peek)
	if peek.InstallPSI && !sc.cluster.CanCreateClusterResource(r.Context(), "machineconfiguration.openshift.io", "machineconfigs") {
		http.Error(w, "not authorized to create machineconfigs", http.StatusForbidden)
		return
	}
	view, err := s.draft.StageEnableDRS(sc.id, sc.proj, raw)
	respond(w, view, err)
}

// handleDRSDisable stages the removal of the KubeDescheduler CR (the operator
// install and any PSI MachineConfig stay committed).
func (s *Server) handleDRSDisable(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.platformScope(w, r, "operator.openshift.io", "kubedeschedulers")
	if !ok {
		return
	}
	view, err := s.draft.StageDisableDRS(sc.id, sc.proj)
	respond(w, view, err)
}
