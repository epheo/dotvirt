package api

import (
	"log"
	"net/http"

	"github.com/epheo/dotvirt/internal/drs"
	"github.com/epheo/dotvirt/internal/model"
)

// The DRS routes are platform-tier: the descheduler rebalances every node, so
// its config commits to the platform repo. The mutating routes gate on their
// verb over the KubeDescheduler CR (enable: its create, or its update when
// DRS is reconfigured); the coordinator checks each file staged.

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
	ctx := r.Context()
	lists := s.capable(ctx, id, c, descheduleRead)
	if s.drs != nil && lists {
		view.Live = s.drs.Live()
	}
	if s.cfg.PlatformRepo != "" {
		enable, psi, disable := s.drsCaps(ctx, id, c)
		platform := s.platformProject()
		if lists {
			if git, err := s.reader.DRSState(platform); err != nil {
				log.Printf("api: drs: read platform repo: %v", err)
				view.Warning = "platform repo unavailable - committed rebalancing state unknown"
			} else {
				view.Configured, view.Config = git.Configured, git.Config
				// A delegate may author the PSI file without listing rendered
				// MachineConfigs, which hold node secrets: it is a fact of git.
				view.PSIConfigured = git.PSIConfigured && (psi || s.capable(ctx, id, c, ask(verbList, ssarMachineCfg, "")))
			}
		}
		if enable || disable {
			if d, err := s.draft.DRSDraft(id, platform); err == nil && (d.Config != nil || d.PSI || d.DisableStaged) {
				view.Draft = &d
			}
		}
	}
	writeJSON(w, view)
}

// descheduleRead is what the rebalancer's state, live or committed, asks.
var descheduleRead = ask(verbList, ssarDescheduler, drs.Namespace)

// listsDescheduler is descheduleRead for a route holding only the request.
func (s *Server) listsDescheduler(r *http.Request) bool {
	id, c, err := s.userCluster(r)
	return err == nil && s.capable(r.Context(), id, c, descheduleRead)
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
	if !ok || !s.gateAny(w, r, sc, drsSchedulerActions()) {
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
