package api

import (
	"encoding/json"
	"net/http"
)

// Node maintenance-lite (the By-Node view): read a node's cordon state and
// toggle it. Nodes are cluster-scoped, so these don't go through resolveProject
// — the user's own token is the gate (a caller without node RBAC gets 403/404,
// and the UI hides the action from NodeInfo.CanCordon).

// handleNodeInfo returns a node's schedulability + whether the caller may cordon it.
func (s *Server) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	_, c, err := s.userCluster(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	info, err := c.NodeInfo(r.Context(), r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleNodeCordon patches node.spec.unschedulable (cordon/uncordon).
func (s *Server) handleNodeCordon(w http.ResponseWriter, r *http.Request) {
	_, c, err := s.userCluster(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Unschedulable bool `json:"unschedulable"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := c.SetNodeCordon(r.Context(), r.PathValue("node"), req.Unschedulable); err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
