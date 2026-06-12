// Package inventory assembles the multi-tenant project tree: for each project the
// caller's token can see, it reads that project's git repo, keeps the VMs in the
// project's namespaces, and enriches them with the caller's live cluster + Argo
// view. It is the shared core behind both the HTTP /api/inventory handler and the
// per-subscriber WebSocket push, so the two never drift.
package inventory

import (
	"time"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// Inputs are the facts the build needs. Live and Drift come from the SA-owned
// shared snapshot (identical for every tenant), filtered to the caller's projects
// by enrich(); Projects is the caller's authorized, repo-resolved set. A nil Drift
// means Argo isn't wired (Sync left unset); a non-nil (possibly empty) Drift means
// it is, so a VM absent from the map is NotTracked.
type Inputs struct {
	Projects []project.ProjectInfo
	Branch   string // repo branch to read (e.g. base/main); empty → repo default
	Repos    *git.RepoSet

	Live  map[string]clusterstate.LiveVM
	Drift map[string]argo.Drift
}

// Build produces the inventory. A project whose repo can't be read (or that has
// no/conflicting repo) is still listed, with its Error set and namespaces empty,
// so a single broken tenant never blanks the whole tree.
func Build(in Inputs) model.Inventory {
	projects := make([]model.Project, 0, len(in.Projects))
	for _, p := range in.Projects {
		projects = append(projects, buildProject(in, p))
	}
	return model.Inventory{Projects: projects}
}

func buildProject(in Inputs, p project.ProjectInfo) model.Project {
	out := model.Project{Name: p.Name, Repo: p.Repo, Error: p.Error, Namespaces: []model.ProjectNamespace{}}
	if p.Repo == "" {
		return out // unresolved repo: Error already explains why
	}

	read, _, err := in.Repos.Get(p.Repo)
	if err != nil {
		out.Error = "repo unavailable: " + err.Error()
		return out
	}
	vms, err := read.ParseVMsOnBranch(in.Branch)
	if err != nil {
		out.Error = "read repo: " + err.Error()
		return out
	}

	allowed := toSet(p.Namespaces)
	byNS := map[string][]model.VM{}
	// Pre-seed the project's namespaces so an empty one still shows as a node
	// (non-nil slice → serializes as [] not null).
	for _, ns := range p.Namespaces {
		byNS[ns] = []model.VM{}
	}
	for _, vm := range vms {
		if _, ok := allowed[vm.Namespace]; !ok {
			continue // manifest for a namespace outside this project; ignore
		}
		enrich(&vm, in)
		byNS[vm.Namespace] = append(byNS[vm.Namespace], vm)
	}

	out.Namespaces = git.GroupNamespaces(byNS)
	return out
}

func enrich(vm *model.VM, in Inputs) {
	k := vm.Namespace + "/" + vm.Name
	if s, ok := in.Live[k]; ok {
		vm.Phase, vm.GuestIP, vm.NodeName = s.Phase, s.GuestIP, s.NodeName
		vm.Paused = s.Paused
		vm.IPs, vm.OS, vm.MemoryActual = s.IPs, s.OS, s.MemoryActual
		if !s.StartedAt.IsZero() {
			vm.StartedAt = s.StartedAt.UTC().Format(time.RFC3339)
		}
		if m := s.Migration; m != nil {
			vm.Migration = &model.Migration{
				SourceNode: m.SourceNode,
				TargetNode: m.TargetNode,
				Completed:  m.Completed,
				Failed:     m.Failed,
			}
			if !m.StartedAt.IsZero() {
				vm.Migration.StartedAt = m.StartedAt.UTC().Format(time.RFC3339)
			}
			if !m.EndedAt.IsZero() {
				vm.Migration.EndedAt = m.EndedAt.UTC().Format(time.RFC3339)
			}
		}
	}
	if in.Drift != nil { // nil = Argo not wired; non-nil = configured (absent VM is NotTracked)
		if d, ok := in.Drift[k]; ok {
			vm.Sync, vm.Health = d.Sync, d.Health
		} else {
			vm.Sync = model.SyncNotTracked
		}
	}
}

func toSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, it := range items {
		out[it] = struct{}{}
	}
	return out
}
