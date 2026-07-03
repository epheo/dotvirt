package api

import (
	"encoding/json"
	"net/http"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/vmtemplate"
)

// Templates are dotvirt's content library: VirtualMachineTemplate manifests
// under templates/ in library repos — every project repo plus the shared
// platform repo. The directory sits outside the ArgoCD-applied path (the CRD
// need not exist on-cluster), so the library is purely a git-plane surface:
// listing parses the SA mirrors, deploying renders in-process and stages the
// VM into the caller's draft — the PR merge stays the apply boundary.

// handleTemplates lists the caller's libraries: their RBAC-visible projects
// plus the shared platform library. The shared library lists for every
// authenticated caller — catalog stance, like /api/options.
func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	projs, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	list := model.TemplateList{Templates: []model.Template{}}
	if s.cfg.PlatformRepo != "" {
		s.appendTemplates(&list, platformProjectName, s.cfg.PlatformRepo)
	}
	for _, p := range projs {
		if p.Repo != "" {
			s.appendTemplates(&list, p.Name, p.Repo)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

// appendTemplates adds one repo's library to the listing. An unreadable repo
// degrades to an absent library — the other libraries still list.
func (s *Server) appendTemplates(list *model.TemplateList, library, repoURL string) {
	read, _, err := s.repos.Get(repoURL)
	if err != nil {
		return
	}
	files, err := read.TemplatesOnBranch(s.cfg.BaseBranch)
	if err != nil {
		return
	}
	for _, f := range files {
		list.Templates = append(list.Templates, vmtemplate.Parse(f.Path, f.Content, library))
	}
}

// handleDeployTemplate renders a library template and stages the VM into the
// target namespace's draft. The gate is the target: deploying is creating a VM
// there (same authorization as POST /api/vms); the library only needs to be
// readable — the caller's own projects or the shared platform library.
func (s *Server) handleDeployTemplate(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req model.DeployTemplateRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.Namespace == "" {
		http.Error(w, "a target namespace is required", http.StatusBadRequest)
		return
	}
	sc, ok := s.resolveProject(w, r, byNamespace(req.Namespace))
	if !ok {
		return
	}
	lib, ok := s.libraryFor(w, r, sc, req.Library)
	if !ok {
		return
	}
	view, err := s.draft.StageDeployFromTemplate(sc.id, sc.proj, lib, req)
	respond(w, view, err)
}

// libraryFor resolves the library a request names: empty or the target's own
// name → the target project; "platform" → the shared library; any other name →
// one of the caller's visible projects (never a repo the caller can't read).
func (s *Server) libraryFor(w http.ResponseWriter, r *http.Request, sc scope, library string) (project.ProjectInfo, bool) {
	switch library {
	case "", sc.proj.Name:
		return sc.proj, true
	case platformProjectName:
		if s.cfg.PlatformRepo == "" {
			http.Error(w, "platform repo not configured (set -platform-repo)", http.StatusServiceUnavailable)
			return project.ProjectInfo{}, false
		}
		return project.ProjectInfo{Name: platformProjectName, Repo: s.cfg.PlatformRepo}, true
	}
	projs, err := s.projectsFor(r.Context(), sc.id, sc.cluster)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return project.ProjectInfo{}, false
	}
	for _, p := range projs {
		if p.Name == library {
			return p, true
		}
	}
	http.Error(w, "library not found", http.StatusNotFound)
	return project.ProjectInfo{}, false
}

// handleSaveTemplate derives a template from a VM's git manifest and stages it
// into a library — "Clone to Template". Saving into the VM's own project needs
// only that project's membership; saving into the shared library gates on the
// virtualmachinetemplates create SSAR (rule-based, so it works before the CRD
// exists on-cluster), like every platform-tier create.
func (s *Server) handleSaveTemplate(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req model.SaveTemplateRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.SourceNamespace == "" || req.SourceName == "" {
		http.Error(w, "a source VM is required", http.StatusBadRequest)
		return
	}
	sc, ok := s.resolveProject(w, r, byNamespace(req.SourceNamespace))
	if !ok {
		return
	}
	commitProj := sc.proj
	switch req.Library {
	case "", sc.proj.Name:
	case platformProjectName:
		psc, ok := s.platformScope(w, r, "template.kubevirt.io", "virtualmachinetemplates")
		if !ok {
			return
		}
		commitProj = psc.proj
	default:
		http.Error(w, "library must be the VM's project or the shared library", http.StatusBadRequest)
		return
	}
	view, err := s.draft.StageSaveTemplate(sc.id, commitProj, sc.proj, req)
	respond(w, view, err)
}
