package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Clone (the Clone action) — the create is imperative + RBAC-gated like
// snapshots, but its outcome is config state: the target VM lands in the
// cluster only, surfacing as NotTracked until adopted into git.

// handleClones lists the VirtualMachineClones whose source is this VM, for the
// Clone modal's progress rows.
func (s *Server) handleClones(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	clones, err := sc.cluster.ListClones(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	respond(w, clones, err)
}

// handleCreateClone clones this VM into a new VM named by the request body.
func (s *Server) handleCreateClone(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	var req struct {
		Target string `json:"target"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	target := strings.TrimSpace(req.Target)
	if target == "" {
		http.Error(w, "target name is required", http.StatusBadRequest)
		return
	}
	if target == name {
		http.Error(w, "target must differ from the source VM name", http.StatusBadRequest)
		return
	}
	// The clone CR's own name just needs uniqueness; the target VM carries the
	// user-chosen name.
	cloneName := "clone-" + target + "-" + time.Now().UTC().Format("20060102-150405")
	err := sc.cluster.CreateClone(r.Context(), ns, name, cloneName, target)
	s.recordTask("Clone", ns, name, sc.id.Username, err == nil)
	if err != nil {
		http.Error(w, err.Error(), runtimeOpStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": cloneName, "target": target})
}
