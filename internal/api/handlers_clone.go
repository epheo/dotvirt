package api

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// Clone (the Clone action) - the create is imperative + RBAC-gated like
// snapshots, but its outcome is config state: the target VM lands in the
// cluster only, surfacing as NotTracked until adopted into git.

// handleClones lists the VirtualMachineClones whose source is this VM, for the
// Clone modal's progress rows.
func (s *Server) handleClones(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	clones, err := sc.cluster.ListClones(r.Context(), ns, name)
	respond(w, clones, err)
}

// handleCreateClone clones this VM into a new VM named by the request body.
func (s *Server) handleCreateClone(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	req, ok := decodeOptional[struct {
		Target string `json:"target"`
	}](w, r)
	if !ok {
		return
	}
	target := strings.TrimSpace(req.Target)
	if target == "" {
		fail(w, invalid(errors.New("target name is required")))
		return
	}
	if target == name {
		fail(w, invalid(errors.New("target must differ from the source VM name")))
		return
	}
	// The clone CR's own name just needs uniqueness; the target VM carries the
	// user-chosen name.
	cloneName := "clone-" + target + "-" + time.Now().UTC().Format("20060102-150405")
	err := sc.cluster.CreateClone(r.Context(), ns, name, cloneName, target)
	s.recordTask("Clone", ns, name, sc.id.Username, err == nil)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": cloneName, "target": target})
}
