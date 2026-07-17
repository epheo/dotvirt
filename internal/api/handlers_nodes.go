package api

import (
	"encoding/json"
	"net/http"
)

// Node maintenance (the By-Node view): read a node's cordon/maintenance state,
// toggle cordon, enter/exit maintenance mode. Nodes are cluster-scoped, so
// these don't go through resolveProject — the user's own token is the gate (a
// caller without node RBAC gets 403/404, and the UI hides the actions from
// NodeInfo.CanCordon).

// handleNodes lists the virtualization hosts (KubeVirt-schedulable nodes) as
// live-migration target candidates. A caller without node-list RBAC gets 403,
// and the migrate dialog offers only scheduler-picked placement.
func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	_, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return
	}
	nodes, err := c.ListNodes(r.Context())
	if err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

// handleNodeInfo returns a node's schedulability + whether the caller may cordon it.
func (s *Server) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	_, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
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
		fail(w, unavailable("cluster access", err))
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

// handleNodeMaintenance enters or exits maintenance mode (annotation + cordon
// in one node patch). Evacuation is not done here: the client drives the
// per-VM migrate calls so each one is gated by that VM's own RBAC and shows up
// as its own action row.
func (s *Server) handleNodeMaintenance(w http.ResponseWriter, r *http.Request) {
	_, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return
	}
	var req struct {
		Enter bool `json:"enter"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := c.SetNodeMaintenance(r.Context(), r.PathValue("node"), req.Enter); err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
