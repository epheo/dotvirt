package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/validate"
)

// The draft routes: stage/unstage/discard/propose against the caller's per-project
// draft, plus the git reads (history) and write-backs (revert, adopt, resync) that
// complete the changeset lifecycle. All are project-scoped via resolveProject.

func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
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
	result, err := s.draft.StageEdit(sc.id, sc.proj, ns, name, req)
	respond(w, result, err)
}

// handleCreate stages a new VM. The path carries no namespace, so we peek the
// spec's namespace to pick the target project.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	raw, p, ok := peek[nsPeek](w, r)
	if !ok {
		return
	}
	if p.Namespace == "" {
		http.Error(w, "spec namespace is required", http.StatusBadRequest)
		return
	}
	sc, ok := s.resolveProject(w, r, byNamespace(p.Namespace))
	if !ok {
		return
	}
	result, err := s.draft.StageCreate(sc.id, sc.proj, raw)
	respond(w, result, err)
}

// handleDelete stages the removal of a VM's manifest into the caller's draft. Like
// edit/adopt it only mutates the user's own draft (no cluster write, no SA
// escalation - Argo prunes the VM on merge under its own RBAC), so namespace
// membership via resolveProject is the right gate, not resync's CanUpdateVM check.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	result, err := s.draft.StageDelete(sc.id, sc.proj, "", ns, name)
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
		// the watch set, and a token that hasn't built an inventory yet isn't in it -
		// so without this its new PR would wait for a later inventory build.
		s.trackProposalsProject(sc.id, sc.proj)
		s.nudgeProposals() // the new PR reaches every lane before the git poll notices
	}
	respond(w, result, err)
}

func (s *Server) handleDrift(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	result, err := s.draft.VMDrift(sc.proj, ns, name)
	respond(w, result, err)
}

func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	result, err := s.draft.Adopt(sc.id, sc.proj, ns, name)
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
	objs, unreadable, ok := s.captureAdoptable(w, r, sc, ns)
	if !ok {
		return
	}
	result, err := s.draft.AdoptObjects(sc.id, sc.proj, ns, objs)
	respond(w, withUnreadable(result, ns, unreadable), err)
}

// captureAdoptable runs the caller-token capture for one namespace ("" = the
// cluster scope, for the platform tier) and applies the shared refusals: no
// ArgoCD picture yet, nothing readable, nothing left to adopt. ok=false means
// the response is written.
func (s *Server) captureAdoptable(w http.ResponseWriter, r *http.Request, sc scope, ns string) ([]changeset.Adoptable, []string, bool) {
	where := ns
	if where == "" {
		where = "the cluster scope"
	}
	// Foreign-app claims only: the own app is not one (git decides what this repo
	// declares; recovery depends on that). Pre-sync would misread every claim as
	// residue, so refuse. nil drift is Argo disabled: no annotation is a live claim.
	var foreignApps map[string]bool
	if s.drift != nil {
		if foreignApps = s.drift.ForeignApps(sc.proj.Repo); foreignApps == nil {
			fail(w, fmt.Errorf("%w: ArgoCD applications not yet loaded", model.ErrUnavailable))
			return nil, nil, false
		}
	}
	var (
		objs       []cluster.Adoptable
		unreadable []string
		err        error
	)
	if ns == "" {
		objs, unreadable, err = sc.cluster.ClusterAdoptableObjects(r.Context(), foreignApps)
	} else {
		objs, unreadable, err = sc.cluster.AdoptableObjects(r.Context(), []string{ns}, foreignApps)
	}
	if err != nil {
		fail(w, err)
		return nil, nil, false
	}
	// "Nothing to adopt" would be a lie when a kind was unreadable, so say what was missed.
	if len(objs) == 0 && len(unreadable) > 0 {
		fail(w, fmt.Errorf("%w: cannot read %s in %s, so there is nothing adoptable you have access to",
			model.ErrForbidden, strings.Join(unreadable, ", "), where))
		return nil, nil, false
	}
	if len(objs) == 0 {
		fail(w, fmt.Errorf("%w: nothing to adopt in %s: everything running there is declared in git or managed by another Application", model.ErrInvalid, where))
		return nil, nil, false
	}
	adoptable := make([]changeset.Adoptable, 0, len(objs))
	for _, o := range objs {
		adoptable = append(adoptable, changeset.Adoptable{
			Namespace: o.Namespace, Name: o.Name, Kind: o.Kind, Path: o.Path, Manifest: o.Manifest,
		})
	}
	return adoptable, unreadable, true
}

// withUnreadable appends the kinds the caller could not read to the view's
// warning (appended, not assigned: the view may carry the derived prune warning).
func withUnreadable(v model.DraftView, where string, unreadable []string) model.DraftView {
	if len(unreadable) > 0 {
		v.Warning = changeset.JoinWarning(v.Warning, fmt.Sprintf("%s: you cannot read %s, so any of those stay outside git.",
			where, strings.Join(unreadable, ", ")))
	}
	return v
}

func (s *Server) handleResync(w http.ResponseWriter, r *http.Request) {
	// Resync runs the reconcile with dotvirt's SA, gated on the caller's OWN
	// authority over the VM (not just namespace read): they may trigger a sync only
	// if they could update the VM themselves - otherwise read access would escalate
	// into an SA-privileged Argo sync. The SSAR runs inside Resync, beside the
	// escalation, so no other caller can reach it unchecked.
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	result, err := s.draft.Resync(r.Context(), sc.cluster.CanUpdateVM, ns, name)
	s.recordTask("Resync", ns, name, sc.id.Username, err == nil)
	respond(w, result, err)
}

// handleManifest returns the VM's manifest file as it exists on the base branch -
// the "Download manifest" action. The git file IS the VM's full definition, so
// this is dotvirt's VM-export path.
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

// handleHistory lists recent commits on the project's base branch - the Changes
// section's history, narrowed to one namespace's directory with ?namespace=.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.pickProject(w, r, r.PathValue("project"))
	if !ok {
		return
	}
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		if err := validate.RequireDNS1123("namespace", ns); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		commits, err := s.draft.NamespaceHistory(sc.proj, ns, 25)
		respond(w, commits, err)
		return
	}
	commits, err := s.draft.History(sc.proj, 25)
	respond(w, commits, err)
}

// handleRestore stages one VM's manifest as a past commit held it - the VM
// page's Restore, which lands in the draft and is proposed like any edit.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	hash, ok := restoreHash(w, r)
	if !ok {
		return
	}
	result, err := s.draft.RestoreVersion(sc.id, sc.proj, "", ns, name, hash)
	respond(w, result, err)
}

// restoreHash reads a restore body's commit hash; ok=false means the response is written.
func restoreHash(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return "", false
	}
	if !commitHash.MatchString(req.Hash) {
		http.Error(w, "commit hash must be the full 40-character hash", http.StatusBadRequest)
		return "", false
	}
	return req.Hash, true
}

// handleVMHistory lists the merged changes to one VM's manifest - the VM page's
// change history, each row deep-linking into the Changes pane's review.
func (s *Server) handleVMHistory(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r)
	if !ok {
		return
	}
	commits, err := s.draft.ObjectHistory(sc.proj, "", ns, name, 10)
	respond(w, commits, err)
}

// commitHash is the only commit reference the history routes accept: the full
// hash a history row carries, so no abbreviation is ever resolved server-side.
var commitHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

// handleCommit renders one past commit as semantic items - the Changes pane's
// review of a merged change, and what reverting it now would do.
func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.pickProject(w, r, r.PathValue("project"))
	if !ok {
		return
	}
	hash := r.PathValue("hash")
	if !commitHash.MatchString(hash) {
		http.Error(w, "commit hash must be the full 40-character hash", http.StatusBadRequest)
		return
	}
	detail, err := s.draft.Commit(sc.proj, hash)
	respond(w, detail, err)
}

// handleProposal renders what merging one open PR would change - the Changes
// pane's review of a proposal, read from the mirrored head branch.
func (s *Server) handleProposal(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.pickProject(w, r, r.PathValue("project"))
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || n <= 0 {
		http.Error(w, "pull request number must be a positive integer", http.StatusBadRequest)
		return
	}
	detail, err := s.draft.Proposal(sc.proj, n)
	if err == nil {
		detail.Proposal.Mine = s.draft.OwnsProposal(sc.id, sc.proj, detail.Proposal.Branch)
	}
	respond(w, detail, err)
}

// handleRevert proposes a forward commit reverting one commit in the project's
// repo - a new PR, never a history rewrite.
func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.pickProject(w, r, r.PathValue("project"))
	if !ok {
		return
	}
	var req struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !commitHash.MatchString(req.Hash) {
		http.Error(w, "commit hash must be the full 40-character hash", http.StatusBadRequest)
		return
	}
	result, err := s.draft.Revert(sc.id, sc.proj, req.Hash)
	if err == nil {
		s.nudgeProposals() // the revert PR reaches every lane before the git poll notices
	}
	respond(w, result, err)
}
