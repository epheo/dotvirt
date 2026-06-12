package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"

	"github.com/epheo/dotvirt/internal/model"
)

// The draft routes: stage/unstage/discard/propose against the caller's per-project
// draft, plus the git reads (history) and write-backs (revert, adopt, resync) that
// complete the changeset lifecycle. All are project-scoped via resolveProject.

func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	var req model.EditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.SourceFile == "" {
		http.Error(w, "sourceFile is required", http.StatusBadRequest)
		return
	}
	result, err := s.draft.StageEdit(sc.id, sc.proj, r.PathValue("namespace"), r.PathValue("name"), req)
	respond(w, result, err)
}

// handleCreate stages a new VM. The path carries no namespace, so we peek the
// spec's namespace to pick the target project.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var peek struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil || peek.Namespace == "" {
		http.Error(w, "spec namespace is required", http.StatusBadRequest)
		return
	}
	sc, ok := s.resolveProject(w, r, byNamespace(peek.Namespace))
	if !ok {
		return
	}
	result, err := s.draft.StageCreate(sc.id, sc.proj, raw)
	respond(w, result, err)
}

// handleDelete stages the removal of a VM's manifest into the caller's draft. Like
// edit/adopt it only mutates the user's own draft (no cluster write, no SA
// escalation — Argo prunes the VM on merge under its own RBAC), so namespace
// membership via resolveProject is the right gate, not resync's CanUpdateVM check.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	result, err := s.draft.StageDelete(sc.id, sc.proj, r.PathValue("namespace"), r.PathValue("name"))
	respond(w, result, err)
}

func (s *Server) handleDraftGet(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.draftScope(w, r)
	if !ok {
		return
	}
	view, err := s.draft.Get(sc.id, sc.proj)
	respond(w, view, err)
}

func (s *Server) handleDraftDiscard(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.draftScope(w, r)
	if !ok {
		return
	}
	if err := s.draft.Discard(sc.id, sc.proj); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUnstage(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	if err := s.draft.Unstage(sc.id, sc.proj, r.PathValue("namespace"), r.PathValue("name")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.draftScope(w, r)
	if !ok {
		return
	}
	var req model.ProposeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.draft.Propose(sc.id, sc.proj, req)
	if err == nil {
		s.nudgeProposals() // the new PR reaches every lane before the git poll notices
	}
	respond(w, result, err)
}

func (s *Server) handleDrift(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	result, err := s.draft.VMDrift(sc.proj, r.PathValue("namespace"), r.PathValue("name"))
	respond(w, result, err)
}

func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	result, err := s.draft.Adopt(sc.id, sc.proj, r.PathValue("namespace"), r.PathValue("name"))
	respond(w, result, err)
}

func (s *Server) handleResync(w http.ResponseWriter, r *http.Request) {
	// Resync runs the reconcile with dotvirt's SA, so gate it on the caller's OWN
	// authority over the VM (not just namespace read): they may trigger a sync only
	// if they could update the VM themselves — otherwise read access would escalate
	// into an SA-privileged Argo sync.
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if allowed, err := sc.cluster.CanUpdateVM(r.Context(), ns, name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !allowed {
		http.Error(w, "you don't have permission to sync this VM", http.StatusForbidden)
		return
	}
	result, err := s.draft.Resync(ns, name)
	respond(w, result, err)
}

// handleManifest returns the VM's manifest file as it exists on the base branch —
// the "Download manifest" action. The git file IS the VM's full definition, so
// this is dotvirt's OVF-export analog.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	sc, ok := s.resolveProject(w, r, byNamespace(ns))
	if !ok {
		return
	}
	if sc.proj.Repo == "" {
		http.Error(w, "project has no repo", http.StatusNotFound)
		return
	}
	read, _, err := s.repos.Get(sc.proj.Repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	vm, found, err := read.FindVMOnBranch(s.cfg.BaseBranch, ns, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "VM is not in git", http.StatusNotFound)
		return
	}
	files, err := read.VMManifests(s.cfg.BaseBranch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, f := range files {
		if f.Path == vm.SourceFile {
			w.Header().Set("Content-Type", "application/yaml")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(f.Path)))
			_, _ = w.Write(f.Content)
			return
		}
	}
	http.Error(w, "manifest file not found", http.StatusNotFound)
}

// handleHistory lists recent commits on the project's base branch — the Changes
// pane's history view.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byName(r.PathValue("project")))
	if !ok {
		return
	}
	if sc.proj.Repo == "" {
		writeJSON(w, http.StatusOK, []model.Commit{})
		return
	}
	read, _, err := s.repos.Get(sc.proj.Repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	commits, err := read.History(s.cfg.BaseBranch, 25)
	respond(w, commits, err)
}

// handleRevert proposes a forward commit reverting one commit in the project's
// repo — a new PR, never a history rewrite.
func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.resolveProject(w, r, byName(r.PathValue("project")))
	if !ok {
		return
	}
	var req struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Hash == "" {
		http.Error(w, "commit hash is required", http.StatusBadRequest)
		return
	}
	result, err := s.draft.Revert(sc.id, sc.proj, req.Hash)
	if err == nil {
		s.nudgeProposals() // the revert PR reaches every lane before the git poll notices
	}
	respond(w, result, err)
}
