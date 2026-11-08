package api

import (
	"log"
	"net/http"

	"github.com/epheo/dotvirt/internal/model"
)

// The DRS routes are platform-tier: the descheduler rebalances every node, so
// its config commits to the platform repo. The mutating routes gate on their
// verb over the KubeDescheduler CR; the coordinator checks each file staged.

// handleDRS reports the DRS tier: the platform repo's committed configuration,
// the caller's staged draft, the live operator state from the SA-watched
// snapshot, and the caller's authoring capability. Snapshot + git-mirror reads
// only - the SSARs ride the per-token cache (the panel polls this endpoint).
// Each plane degrades independently: a git-side failure becomes a Warning on
// an otherwise-served view, never a 500 that hides the live state.
func (s *Server) handleDRS(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	var view model.DRSView
	if s.drs != nil {
		view.Live = s.drs.Live()
	}
	if s.cfg.PlatformRepo != "" {
		ctx := r.Context()
		verdicts := s.capableAll(ctx, id, c, []action{drsEnableAction(false), drsEnableAction(true), drsDisableAction()})
		view.CanManage, view.CanPSI, view.CanDisable = verdicts[0], verdicts[1], verdicts[2]
		platform := s.platformProject()
		if git, err := s.reader.DRSState(platform); err != nil {
			log.Printf("api: drs: read platform repo: %v", err)
			view.Warning = "platform repo unavailable - committed DRS state unknown"
		} else {
			view.Configured, view.Config, view.PSIConfigured = git.Configured, git.Config, git.PSIConfigured
		}
		if view.CanManage || view.CanDisable {
			if d, err := s.draft.DRSDraft(id, platform); err == nil && (d.Config != nil || d.PSI || d.DisableStaged) {
				view.Draft = &d
			}
		}
	}
	writeJSON(w, view)
}

// handleDRSEnable stages the DRS (descheduler) file set - operator install +
// KubeDescheduler CR, optionally the PSI MachineConfig, which reboots the
// worker pool when merged - into the platform draft.
func (s *Server) handleDRSEnable(w http.ResponseWriter, r *http.Request) {
	raw, _, ok := readBody[struct{}](w, r, false)
	if !ok {
		return
	}
	sc, ok := s.platformScopeGated(w, r)
	if !ok || !s.gate(w, r, sc, drsCreateAction()) {
		return
	}
	view, err := s.draft.StageEnableDRS(sc.id, sc.proj, raw)
	respond(w, view, err)
}

// handleDRSDisable stages the removal of the KubeDescheduler CR (the operator
// install and any PSI MachineConfig stay committed).
func (s *Server) handleDRSDisable(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.platformScopeGated(w, r)
	if !ok || !s.gate(w, r, sc, drsDisableAction()) {
		return
	}
	view, err := s.draft.StageDisableDRS(sc.id, sc.proj)
	respond(w, view, err)
}
