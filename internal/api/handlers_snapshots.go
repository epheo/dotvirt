package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// Snapshots (the Snapshots tab) — imperative + RBAC-gated like the runtime ops,
// not git-managed, so ArgoCD never reverts them.

func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	snaps, err := sc.cluster.ListSnapshots(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	respond(w, snaps, err)
}

// handleTakeSnapshot creates a point-in-time snapshot; a name may be supplied, else
// one is derived from the VM name + timestamp.
func (s *Server) handleTakeSnapshot(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	snapName := req.Name
	if snapName == "" {
		snapName = name + "-" + time.Now().UTC().Format("20060102-150405")
	}
	err := sc.cluster.CreateSnapshot(r.Context(), ns, name, snapName)
	s.recordTask("Snapshot", ns, name, sc.id.Username, err == nil)
	if err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": snapName})
}

// handleRestoreSnapshot rolls the VM back to a snapshot (the VM must be stopped).
func (s *Server) handleRestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	err := sc.cluster.RestoreSnapshot(r.Context(), r.PathValue("namespace"), r.PathValue("name"), r.PathValue("snapshot"))
	s.recordTask("Restore snapshot", r.PathValue("namespace"), r.PathValue("name"), sc.id.Username, err == nil)
	if err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	if err := sc.cluster.DeleteSnapshot(r.Context(), r.PathValue("namespace"), r.PathValue("snapshot")); err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
