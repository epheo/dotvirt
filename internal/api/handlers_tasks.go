package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
	"github.com/epheo/dotvirt/pkg/forge"
)

// The Recent Tasks feed. Serving is a pure in-memory read scoped to the caller:
// ops by visible namespace (node ops by the node-read signal), merges by visible
// project repo. TasksVersion on the inventory frame tells clients when to
// re-pull; the webhook and the proposals refresher keep the feed itself fresh.

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil {
		writeJSON(w, http.StatusOK, []model.TaskEntry{})
		return
	}
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable("cluster access", err))
		return
	}
	projects, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return
	}
	if s.canAuthorPlatform(r.Context(), id, c) {
		projects = append(projects, project.ProjectInfo{Name: platformProjectName, Repo: s.cfg.PlatformRepo})
	}
	canNodes := s.canReadNodesCached(r.Context(), id, c)
	writeJSON(w, http.StatusOK, scopeTasks(s.tasks.Ops(), s.tasks.Merges(), projects, canNodes))
}

// recordTask logs one imperative act into the feed. Nil-safe so handlers need no
// guard (tests and degraded wiring construct the Server without a feed).
func (s *Server) recordTask(verb, namespace, name, by string, ok bool) {
	if s.tasks == nil {
		return
	}
	s.tasks.RecordOp(tasks.Op{Verb: verb, Namespace: namespace, Name: name, By: by, OK: ok, At: time.Now()})
}

// scopeTasks projects the feed onto one caller's visibility: ops in a namespace
// they can read (namespace-less node ops behind the node-read signal), merges in
// a repo backing one of their projects. Pure, so it unit-tests without a Server.
func scopeTasks(ops []tasks.Op, merges []tasks.Merge, projects []project.ProjectInfo, canNodes bool) []model.TaskEntry {
	nsProject := map[string]string{}
	repoProject := map[string]string{}
	for _, p := range projects {
		for _, ns := range p.Namespaces {
			nsProject[ns] = p.Name
		}
		if p.Repo != "" {
			repoProject[forge.NormalizeRepoURL(p.Repo)] = p.Name
		}
	}
	out := []model.TaskEntry{}
	for _, o := range ops {
		proj := ""
		if o.Namespace == "" {
			if !canNodes {
				continue
			}
		} else if proj = nsProject[o.Namespace]; proj == "" {
			continue
		}
		out = append(out, model.TaskEntry{
			Kind: "op", Verb: o.Verb, Namespace: o.Namespace, Name: o.Name,
			Project: proj, By: o.By, OK: o.OK, At: o.At.UTC().Format(time.RFC3339),
		})
	}
	for _, m := range merges {
		proj, ok := repoProject[m.RepoURL]
		if !ok {
			continue
		}
		out = append(out, model.TaskEntry{
			Kind: "merge", Verb: "Merged", Project: proj, PRNumber: m.Number,
			PRURL: m.URL, Title: m.Title, By: m.By, OK: true, At: m.At.UTC().Format(time.RFC3339),
		})
	}
	// All timestamps are self-formatted RFC3339 UTC, so lexicographic order is
	// chronological.
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}
