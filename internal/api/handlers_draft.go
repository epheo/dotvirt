package api

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/validate"
)

// The draft routes: stage/unstage/discard/propose against the caller's per-project
// draft, plus the git reads (history) and write-backs (revert, adopt, resync) that
// complete the changeset lifecycle. All are project-scoped via resolveProject.

func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r, dotvirtReads)
	if !ok || !s.gate(w, r, sc, vmAction(verbUpdate, ns)) {
		return
	}
	req, ok := decode[model.EditRequest](w, r)
	if !ok {
		return
	}
	result, err := s.draft.StageEdit(sc.id, sc.proj, ns, name, req)
	respond(w, result, err)
}

// handleEditPreview answers what staging an edit would change, without staging
// it. Same gates as handleEdit: only someone who may stage asks.
func (s *Server) handleEditPreview(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r, dotvirtReads)
	if !ok || !s.gate(w, r, sc, vmAction(verbUpdate, ns)) {
		return
	}
	edit, ok := decode[model.VMEdit](w, r)
	if !ok {
		return
	}
	changes, err := s.draft.PreviewEdit(sc.id, sc.proj, ns, name, edit)
	respond(w, changes, err)
}

// handleCreate stages a new VM. The path carries no namespace, so we peek the
// spec's namespace to pick the target project.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	raw, p, ok := readBody[nsPeek](w, r, false)
	if !ok {
		return
	}
	if p.Namespace == "" {
		fail(w, invalid(errors.New("spec namespace is required")))
		return
	}
	sc, ok := s.resolveProject(w, r, byNamespace(p.Namespace))
	if !ok || !s.gate(w, r, sc, vmAction(verbCreate, p.Namespace)) {
		return
	}
	if isDryRun(r) {
		p, err := s.draft.PreviewCreateVM(sc.id, sc.proj, raw)
		respond(w, p, err)
		return
	}
	result, err := s.draft.StageCreateVM(sc.id, sc.proj, raw)
	respond(w, result, err)
}

// handleDelete stages the removal of a VM's manifest into the caller's draft. Like
// edit/adopt it only mutates the user's own draft; Argo prunes the VM on merge
// under its own RBAC, so the gate is the caller's own delete right.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r, dotvirtReads)
	if !ok || !s.gate(w, r, sc, vmAction(verbDelete, ns)) {
		return
	}
	result, err := s.draft.StageDelete(sc.id, sc.proj, "", ns, name)
	respond(w, result, err)
}

// handleDrafts answers, in one call, each draft of the caller's the draft
// route would: their visible projects', and the platform tier's past
// canDraftPlatform. A review failure there leaves the platform out rather
// than failing every lane: it denies only a caller holding nothing there.
func (s *Server) handleDrafts(w http.ResponseWriter, r *http.Request) {
	sc, projects, ok := s.callerProjects(w, r)
	if !ok {
		return
	}
	if s.cfg.PlatformRepo != "" {
		if ok, _ := s.canDraftPlatform(r.Context(), sc.id, sc.cluster); ok {
			projects = append(projects, s.platformProject())
		}
	}
	rendered, err := s.draft.Drafts(sc.id, projects)
	if err != nil {
		fail(w, err)
		return
	}
	out := model.DraftsView{Drafts: []model.ProjectDraft{}, Failed: []model.ProjectError{}}
	for _, d := range rendered {
		if d.Err != nil {
			out.Failed = append(out.Failed, model.ProjectError{Project: d.Project, Error: publicMessage(d.Err)})
			continue
		}
		out.Drafts = append(out.Drafts, model.ProjectDraft{Project: d.Project, Draft: d.View})
	}
	writeJSON(w, out)
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
		sc, ok = s.pickDraft(w, r, p) // platform tier is gated; tenants resolve by name
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

// handlePropose opens a PR from the caller's draft of a project they see. The
// coordinator weighs every entry again under the caller's token; a project
// library entry, which no gate weighs, is weighed again here.
func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.draftScope(w, r)
	if !ok {
		return
	}
	if sc.proj.Name != platformProjectName {
		view, err := s.draft.Get(sc.id, sc.proj)
		if err != nil {
			fail(w, err)
			return
		}
		if touchesLibrary(view.Items) && !s.authorizeLibrary(w, r, sc) {
			return
		}
	}
	req, ok := decode[model.ProposeRequest](w, r)
	if !ok {
		return
	}
	result, err := s.draft.Propose(sc.id, sc.proj, req)
	if err == nil {
		s.proposalOpened(sc)
	}
	respond(w, result, err)
}

// handleDrift answers the VM detail's drift, with the caller's verdict on
// resyncing the VM, weighed on the resync route's own path.
func (s *Server) handleDrift(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r, dotvirtReads)
	if !ok {
		return
	}
	result, err := s.reader.VMDrift(sc.proj, ns, name)
	if err == nil && s.drift != nil {
		need, nerr := s.draft.ResyncNeeds(sc.proj, ns, name)
		result.CanResync = s.needsAllowed(r.Context(), sc.id, sc.cluster, need, nerr)
	}
	respond(w, result, err)
}

func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r, dotvirtReads)
	if !ok || !s.gate(w, r, sc, vmAction(verbUpdate, ns)) {
		return
	}
	result, err := s.draft.Adopt(sc.id, sc.proj, ns, name)
	respond(w, result, err)
}

// handleAdoptNamespace brings a whole namespace under GitOps in one draft: everything
// it runs that git does not describe, not only its VMs, so the namespace does not end
// up half declared. The capture runs under the caller's own token, so a user adopts
// exactly what their RBAC lets them read, of the objects the gate would let
// them bring under git (adoptableByRight). One draft, proposed as one PR.
func (s *Server) handleAdoptNamespace(w http.ResponseWriter, r *http.Request) {
	sc, ns, _, ok := s.vmScope(w, r, dotvirtReads)
	if !ok {
		return
	}
	objs, unreadable, ok := s.captureAdoptable(w, r, sc, ns)
	if !ok {
		return
	}
	objs, denied, ok := s.adoptableByRight(w, r, sc, ns, objs)
	if !ok {
		return
	}
	result, err := s.draft.AdoptObjects(sc.id, sc.proj, ns, objs)
	respond(w, withUnreadable(result, ns, unreadable, denied), err)
}

// captureAdoptable runs the caller-token capture for one namespace ("" = the
// cluster scope, for the platform tier) and applies the shared refusals: no
// ArgoCD picture yet, nothing readable, nothing left to adopt. ok=false means
// the response is written.
func (s *Server) captureAdoptable(w http.ResponseWriter, r *http.Request, sc scope, ns string) ([]model.Adoptable, []string, bool) {
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
		objs       []model.Adoptable
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
	return objs, unreadable, true
}

// withUnreadable appends the kinds the caller could not read, and those they
// may not create, to the view's warning (appended, not assigned: the view may
// carry the derived prune warning).
func withUnreadable(v model.DraftView, where string, unreadable, denied []string) model.DraftView {
	if len(unreadable) > 0 {
		v.Warning = changeset.JoinWarning(v.Warning, fmt.Sprintf("%s: you cannot read %s, so any of those stay outside git.",
			where, strings.Join(unreadable, ", ")))
	}
	if len(denied) > 0 {
		v.Warning = changeset.JoinWarning(v.Warning, fmt.Sprintf("%s: you may not create %s, so those stay outside git.",
			where, strings.Join(denied, ", ")))
	}
	return v
}

func (s *Server) handleResync(w http.ResponseWriter, r *http.Request) {
	// Resync runs the reconcile with dotvirt's SA, gated on the caller's OWN
	// authority over what the sync applies (not just namespace read), weighed
	// inside Resync beside the escalation, so no other caller can reach it
	// unchecked.
	sc, ns, name, ok := s.vmScope(w, r, dotvirtReads)
	if !ok {
		return
	}
	result, err := s.draft.Resync(r.Context(), sc.id, sc.proj, ns, name)
	s.recordTask("Resync", ns, name, sc.id.Username, err == nil)
	respond(w, result, err)
}

// handleManifest returns the VM's manifest file as it exists on the base branch -
// the "Download manifest" action. The git file IS the VM's full definition, so
// this is dotvirt's VM-export path.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r, dotvirtReads)
	if !ok {
		return
	}
	p, content, err := s.reader.Manifest(sc.proj, ns, name)
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
			fail(w, err)
			return
		}
		if sc.proj.Name != platformProjectName && !slices.Contains(sc.proj.Namespaces, ns) {
			fail(w, fmt.Errorf("%w: namespace %q in project %s", model.ErrNotFound, ns, sc.proj.Name))
			return
		}
		if !s.gateRepo(w, r, sc, ns) {
			return
		}
		commits, err := s.reader.NamespaceHistory(sc.proj, ns, 25)
		respond(w, commits, err)
		return
	}
	if !s.gateRepo(w, r, sc, "") {
		return
	}
	commits, err := s.reader.History(sc.proj, 25)
	respond(w, commits, err)
}

// handleRestore stages one VM's manifest as a past commit held it - the VM
// page's Restore, which lands in the draft and is proposed like any edit.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r, dotvirtReads)
	if !ok || !s.gate(w, r, sc, vmAction(verbUpdate, ns)) {
		return
	}
	hash, ok := hashBody(w, r)
	if !ok {
		return
	}
	result, err := s.draft.RestoreVersion(sc.id, sc.proj, "", ns, name, hash)
	respond(w, result, err)
}

// touchesLibrary reports whether items write a template library.
func touchesLibrary(items []model.DraftItem) bool {
	for _, it := range items {
		if it.Resource == string(draft.ResourceTemplate) {
			return true
		}
	}
	return false
}

// hashBody reads the commit hash a restore or revert body names; ok=false means
// the response is written.
func hashBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	req, ok := decode[struct {
		Hash string `json:"hash"`
	}](w, r)
	if !ok {
		return "", false
	}
	if !commitHash.MatchString(req.Hash) {
		fail(w, invalid(errors.New("commit hash must be the full 40-character hash")))
		return "", false
	}
	return req.Hash, true
}

// handleVMHistory lists the merged changes to one VM's manifest - the VM page's
// change history, each row deep-linking into the Changes pane's review.
func (s *Server) handleVMHistory(w http.ResponseWriter, r *http.Request) {
	sc, ns, name, ok := s.vmScope(w, r, dotvirtReads)
	if !ok {
		return
	}
	commits, err := s.reader.ObjectHistory(sc.proj, "", ns, name, 10)
	respond(w, commits, err)
}

// commitHash is the only commit reference the history routes accept: the full
// hash a history row carries, so no abbreviation is ever resolved server-side.
var commitHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

// handleCommit renders one past commit as semantic items - the Changes pane's
// review of a merged change, and what reverting it now would do.
func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.pickProject(w, r, r.PathValue("project"))
	if !ok || !s.gateRepo(w, r, sc, "") {
		return
	}
	hash := r.PathValue("hash")
	if !commitHash.MatchString(hash) {
		fail(w, invalid(errors.New("commit hash must be the full 40-character hash")))
		return
	}
	detail, err := s.reader.Commit(sc.proj, hash)
	respond(w, detail, err)
}

// handleProposal renders what merging one open PR would change - the Changes
// pane's review of a proposal, read from the mirrored head branch. A caller
// who may not read the repo reviews their own proposals there alone
// (ownProposal).
func (s *Server) handleProposal(w http.ResponseWriter, r *http.Request) {
	var (
		sc      scope
		ok      bool
		refused error
	)
	if p := r.PathValue("project"); p == platformProjectName {
		var reads bool
		if sc, reads, ok = s.platformReadScope(w, r); ok && !reads {
			refused = fmt.Errorf("%w: %s", model.ErrForbidden, platformReadDenied)
		}
	} else if sc, ok = s.pickProject(w, r, p); ok {
		refused = s.readsRepo(r.Context(), sc, "")
	}
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || n <= 0 {
		fail(w, invalid(errors.New("pull request number must be a positive integer")))
		return
	}
	if refused != nil {
		s.ownProposal(w, sc, n, refused)
		return
	}
	detail, err := s.reader.Proposal(sc.proj, n)
	if err == nil {
		detail.Proposal.Mine = s.reader.OwnsProposal(sc.id, sc.proj, detail.Proposal.Branch)
	}
	respond(w, detail, err)
}

// ownProposal answers a review of a repo the caller may not read. Their own
// draft's open proposal opens, off the cached lane; anything else is the
// repo's refusal, the same for another's PR, a closed one or none, before any
// forge call. The items show only while dotvirt wrote every commit on the
// branch for them: anyone else's push may name what they cannot read.
func (s *Server) ownProposal(w http.ResponseWriter, sc scope, n int, refused error) {
	if !errors.Is(refused, model.ErrForbidden) {
		fail(w, refused)
		return
	}
	row, ok := s.ownRow(sc.id, sc.proj, n)
	if !ok {
		fail(w, refused)
		return
	}
	detail, err := s.reader.OwnProposal(sc.id, sc.proj, row)
	if errors.Is(err, git.ErrForeignCommit) {
		err = fmt.Errorf("%w: pull request #%d changed outside the console since you proposed it; someone who can read the whole repo can review it", model.ErrForbidden, n)
	}
	detail.Proposal.Mine = true
	respond(w, detail, err)
}

// handleRevert proposes a forward commit reverting one commit in the project's
// repo - a new PR, never a history rewrite. The coordinator weighs each object
// it changes; a project library file, which no gate weighs, is weighed here.
func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.pickProject(w, r, r.PathValue("project"))
	if !ok || !s.gateRepo(w, r, sc, "") {
		return
	}
	hash, ok := hashBody(w, r)
	if !ok {
		return
	}
	if sc.proj.Name != platformProjectName {
		detail, err := s.reader.Commit(sc.proj, hash)
		if err != nil {
			fail(w, err)
			return
		}
		if touchesLibrary(detail.Items) && !s.authorizeLibrary(w, r, sc) {
			return
		}
	}
	result, err := s.draft.Revert(sc.id, sc.proj, hash)
	if err == nil {
		s.proposalOpened(sc)
	}
	respond(w, result, err)
}
