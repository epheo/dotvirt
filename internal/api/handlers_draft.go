package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/epheo/dotvirt/internal/changeset"
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
		fail(w, invalid(err))
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
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUnstage(w http.ResponseWriter, r *http.Request) {
	// Cluster-scoped entries (a CUDN/NNCP under the "cluster" sentinel namespace)
	// can't be resolved by namespace, so they carry the target project explicitly.
	var sc scope
	var ok bool
	if p := r.URL.Query().Get("project"); p != "" {
		sc, ok = s.pickProject(w, r, p) // platform tier is gated; tenants resolve by name
	} else {
		sc, ok = s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	}
	if !ok {
		return
	}
	if err := s.draft.Unstage(sc.id, sc.proj, r.URL.Query().Get("resource"), r.PathValue("namespace"), r.PathValue("name")); err != nil {
		fail(w, err)
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
		// Track this project first: the nudge below only refreshes tokens already in
		// the watch set, and a token that hasn't built an inventory yet isn't in it —
		// so without this its new PR would wait for a later inventory build.
		s.trackProposalsProject(sc.id, sc.proj)
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

// handleAdoptNamespace brings a whole namespace under GitOps in one draft: everything
// it runs that git does not describe, not only its VMs, so the namespace does not end
// up half declared. The capture runs under the caller's own token, so a user adopts
// exactly what their RBAC lets them read. One draft, proposed as one PR.
func (s *Server) handleAdoptNamespace(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	sc, ok := s.resolveProject(w, r, byNamespace(ns))
	if !ok {
		return
	}
	// Foreign-app claims only: the own app is not one (git decides what this repo
	// declares; recovery depends on that). Pre-sync would misread every claim as
	// residue, so refuse. nil drift is Argo disabled: no annotation is a live claim.
	var foreignApps map[string]bool
	if s.drift != nil {
		if foreignApps = s.drift.ForeignApps(sc.proj.Repo); foreignApps == nil {
			fail(w, fmt.Errorf("%w: ArgoCD applications not yet loaded", model.ErrUnavailable))
			return
		}
	}
	objs, unreadable, err := sc.cluster.AdoptableObjects(r.Context(), []string{ns}, foreignApps)
	if err != nil {
		fail(w, err)
		return
	}
	// "Nothing to adopt" would be a lie when a kind was unreadable, so say what was missed.
	if len(objs) == 0 && len(unreadable) > 0 {
		fail(w, fmt.Errorf("%w: cannot read %s in %s, so there is nothing adoptable you have access to",
			model.ErrForbidden, strings.Join(unreadable, ", "), ns))
		return
	}
	if len(objs) == 0 {
		fail(w, fmt.Errorf("%w: nothing to adopt in %s: everything running there is declared in git or managed by another Application", model.ErrInvalid, ns))
		return
	}
	adoptable := make([]changeset.Adoptable, 0, len(objs))
	for _, o := range objs {
		adoptable = append(adoptable, changeset.Adoptable{
			Namespace: o.Namespace, Name: o.Name, Kind: o.Kind, Path: o.Path, Manifest: o.Manifest,
		})
	}
	result, err := s.draft.AdoptNamespace(sc.id, sc.proj, ns, adoptable)
	if err == nil && len(unreadable) > 0 {
		// Appended, not assigned: the view may already carry the derived prune warning.
		result.Warning = changeset.JoinWarning(result.Warning, fmt.Sprintf("%s: you cannot read %s, so any of those stay outside git.",
			ns, strings.Join(unreadable, ", ")))
	}
	respond(w, result, err)
}

func (s *Server) handleResync(w http.ResponseWriter, r *http.Request) {
	// Resync runs the reconcile with dotvirt's SA, gated on the caller's OWN
	// authority over the VM (not just namespace read): they may trigger a sync only
	// if they could update the VM themselves — otherwise read access would escalate
	// into an SA-privileged Argo sync. The SSAR runs inside Resync, beside the
	// escalation, so no other caller can reach it unchecked.
	sc, ok := s.resolveProject(w, r, byNamespace(r.PathValue("namespace")))
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	result, err := s.draft.Resync(r.Context(), sc.cluster.CanUpdateVM, ns, name)
	s.recordTask("Resync", ns, name, sc.id.Username, err == nil)
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
	p, content, err := s.draft.Manifest(sc.proj, ns, name)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(p)))
	_, _ = w.Write(content)
}

// handleHistory lists recent commits on the project's base branch — the Changes
// pane's history view.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.pickProject(w, r, r.PathValue("project"))
	if !ok {
		return
	}
	commits, err := s.draft.History(sc.proj, 25)
	respond(w, commits, err)
}

// handleRevert proposes a forward commit reverting one commit in the project's
// repo — a new PR, never a history rewrite.
func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.pickProject(w, r, r.PathValue("project"))
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
