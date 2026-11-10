package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
	"github.com/epheo/dotvirt/pkg/forge"
)

// The Recent Tasks feed. Serving is a pure in-memory read scoped to the caller:
// ops by visible namespace (node ops by the node-read signal), merges by visible
// project repo. TasksVersion on the inventory frame tells clients when to
// re-pull (tasksWatermark); the webhook and the proposals refresher keep the
// feed itself fresh.

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil {
		writeJSON(w, []model.TaskEntry{})
		return
	}
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	projects, err := s.projectsFor(r.Context(), id, c)
	if err != nil {
		fail(w, err)
		return
	}
	platform, _ := s.canReadPlatform(r.Context(), id, c)
	canNodes := s.capable(r.Context(), id, c, ask(verbList, ssarNodes, ""))
	writeJSON(w, scopeTasks(s.tasks.Ops(), s.tasks.Merges(), s.taskScope(id, projects, platform, canNodes)))
}

// recordTask logs one imperative act into the feed. Nil-safe so handlers need no
// guard (tests and degraded wiring construct the Server without a feed).
func (s *Server) recordTask(verb, namespace, name, by string, ok bool) {
	if s.tasks == nil {
		return
	}
	s.tasks.RecordOp(tasks.Op{Verb: verb, Namespace: namespace, Name: name, By: by, OK: ok, At: time.Now()})
}

// taskScope is one caller's view of the feed: ops in a namespace they can read
// (namespace-less node ops behind the node-read signal), merges in a repo
// backing one of their projects. The served feed and the frame's TasksVersion
// both read through it, so the watermark moves exactly when the feed does.
// own is, by repo, the project and draft branch of a caller who may not see
// all of it: the platform repo when they do not read it, a project's when they
// see part of it. Its merges show only from that branch.
type taskScope struct {
	nsProject, repoProject map[string]string
	canNodes               bool
	own                    map[string]ownLane
}

// taskScope is id's view of the feed over their projects: every platform
// merge for a platform reader, their own draft's for anyone else.
func (s *Server) taskScope(id auth.Identity, projects []project.ProjectInfo, readsPlatform, canNodes bool) taskScope {
	platform := s.platformProject()
	if readsPlatform {
		projects = append(projects[:len(projects):len(projects)], platform)
	}
	sc := newTaskScope(projects, canNodes)
	if !readsPlatform && platform.Repo != "" {
		sc.own[forge.NormalizeRepoURL(platform.Repo)] = ownLane{platform.Name, s.reader.ProposedBranch(id, platform)}
	}
	partial := s.partialProjects(projects)
	for _, p := range projects {
		if partial[p.Name] && p.Repo != "" {
			repo := forge.NormalizeRepoURL(p.Repo)
			delete(sc.repoProject, repo)
			sc.own[repo] = ownLane{p.Name, s.reader.ProposedBranch(id, p)}
		}
	}
	return sc
}

type ownLane struct{ project, branch string }

func newTaskScope(projects []project.ProjectInfo, canNodes bool) taskScope {
	sc := taskScope{nsProject: map[string]string{}, repoProject: map[string]string{}, own: map[string]ownLane{}, canNodes: canNodes}
	for _, p := range projects {
		for _, ns := range p.Namespaces {
			sc.nsProject[ns] = p.Name
		}
		if p.Repo != "" {
			sc.repoProject[forge.NormalizeRepoURL(p.Repo)] = p.Name
		}
	}
	return sc
}

// op reports whether the caller sees o, and the project it shows under.
func (sc taskScope) op(o tasks.Op) (project string, ok bool) {
	if o.Namespace == "" {
		return "", sc.canNodes
	}
	project = sc.nsProject[o.Namespace]
	return project, project != ""
}

// merge reports whether the caller sees m, and the project it shows under.
func (sc taskScope) merge(m tasks.Merge) (project string, ok bool) {
	if project, ok = sc.repoProject[m.RepoURL]; ok {
		return project, true
	}
	if own, ok := sc.own[m.RepoURL]; ok && own.branch != "" && m.Branch == own.branch {
		return own.project, true
	}
	return "", false
}

// scopeTasks projects the feed onto one caller's visibility. Pure, so it
// unit-tests without a Server.
func scopeTasks(ops []tasks.Op, merges []tasks.Merge, sc taskScope) []model.TaskEntry {
	out := []model.TaskEntry{}
	for _, o := range ops {
		proj, ok := sc.op(o)
		if !ok {
			continue
		}
		out = append(out, model.TaskEntry{
			Kind: "op", Verb: o.Verb, Namespace: o.Namespace, Name: o.Name,
			Project: proj, By: o.By, OK: o.OK, At: o.At.UTC().Format(time.RFC3339),
		})
	}
	for _, m := range merges {
		proj, ok := sc.merge(m)
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
