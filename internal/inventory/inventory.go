// Package inventory assembles the multi-tenant project tree: for each project the
// caller's token can see, it reads that project's git repo, keeps the VMs in the
// project's namespaces, and enriches them with the caller's live cluster + Argo
// view. It is the shared core behind both the HTTP /api/inventory handler and the
// per-subscriber WebSocket push, so the two never drift.
package inventory

import (
	"sort"
	"strings"
	"time"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// Inputs are the facts the build needs. Live and Drift come from the SA-owned
// shared snapshot (identical for every tenant), filtered to the caller's projects
// by enrich(); Projects is the caller's authorized, repo-resolved set. A nil Drift
// means Argo isn't wired (Sync left unset); a non-nil (possibly empty) Drift means
// it is, so a VM absent from the map is NotTracked.
type Inputs struct {
	Projects []project.ProjectInfo
	Branch   string // repo branch to read (e.g. base/main); empty -> repo default
	Repos    *git.RepoSet

	Live  map[string]clusterstate.LiveVM
	Drift map[string]argo.Drift
	// ProjectDrift is each project's ArgoCD Application rollup keyed by canonical
	// repoURL. Nil (like Drift) means Argo isn't wired or hasn't synced; non-nil with a
	// repo absent means no Application manages it (GitOps left nil, no alarm).
	ProjectDrift map[string]model.ProjectSync
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
	// The GitOps rollup is keyed by the Application's repo, independent of whether
	// dotvirt can read the repo - so it's attached even when the repo read below fails
	// (that's exactly when a "sync unhealthy" badge matters most).
	if in.ProjectDrift != nil && p.Repo != "" {
		if ps, ok := in.ProjectDrift[forge.NormalizeRepoURL(p.Repo)]; ok {
			out.GitOps = &ps
		}
	}
	// A dead or unresolved repo must not blind the inventory: live objects are
	// cluster facts. The error becomes a badge; namespaces and running VMs still
	// render (NotTracked). Hiding them made stranded and not-yet-managed projects
	// look empty and invited the wrong recovery actions.
	var vms []model.VM
	if p.Repo != "" {
		read, _, err := in.Repos.Get(p.Repo)
		if err != nil {
			out.Error = "repo unavailable: " + err.Error()
		} else if vms, err = read.ParseVMsOnBranch(in.Branch); err != nil {
			out.Error = "read repo: " + err.Error()
		}
	}

	allowed := toSet(p.Namespaces)
	byNS := map[string][]model.VM{}
	// Pre-seed the project's namespaces so an empty one still shows as a node
	// (non-nil slice -> serializes as [] not null).
	for _, ns := range p.Namespaces {
		byNS[ns] = []model.VM{}
	}
	inGit := map[string]struct{}{}
	for _, vm := range vms {
		if _, ok := allowed[vm.Namespace]; !ok {
			continue // manifest for a namespace outside this project; ignore
		}
		inGit[vm.Namespace+"/"+vm.Name] = struct{}{}
		enrich(&vm, in)
		byNS[vm.Namespace] = append(byNS[vm.Namespace], vm)
	}

	// Cluster-only VMs: live in a project namespace but absent from git on the
	// base branch (a fresh clone target, an out-of-band create). Shown so the
	// inventory matches the cluster and "Adopt into git" has a row to act on.
	// NotTracked by definition (nothing on main for Argo to manage); desired
	// state doesn't exist, so Power is Unknown and config fields stay empty -
	// an empty SourceFile is the UI's "not in git" marker.
	for k, live := range in.Live {
		ns, name, ok := splitKey(k)
		if !ok {
			continue
		}
		if _, inProject := allowed[ns]; !inProject {
			continue
		}
		if _, tracked := inGit[k]; tracked {
			continue
		}
		vm := model.VM{Namespace: ns, Name: name, Power: model.PowerUnknown, Sync: model.SyncNotTracked}
		applyLive(&vm, live)
		byNS[ns] = append(byNS[ns], vm)
	}

	out.Namespaces = git.GroupNamespaces(byNS)
	return out
}

// splitKey splits a "namespace/name" snapshot key.
func splitKey(k string) (ns, name string, ok bool) {
	return strings.Cut(k, "/")
}

// Adoptable finds the namespaces holding VMs but carrying no project label -
// tenants that predate dotvirt on this cluster. labeled is the project-labeled
// namespace set (the SA snapshot's namespace watch is label-selected, so it IS
// that set); system namespaces are skipped.
func Adoptable(live map[string]clusterstate.LiveVM, labeled []project.Namespace) []model.AdoptableNamespace {
	owned := make(map[string]bool, len(labeled))
	for _, ns := range labeled {
		owned[ns.Name] = true
	}
	counts := map[string]int{}
	for k := range live {
		ns, _, ok := splitKey(k)
		if !ok || owned[ns] || isSystemNamespace(ns) {
			continue
		}
		counts[ns]++
	}
	out := make([]model.AdoptableNamespace, 0, len(counts))
	for ns, n := range counts {
		out = append(out, model.AdoptableNamespace{Namespace: ns, VMs: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace < out[j].Namespace })
	return out
}

func isSystemNamespace(ns string) bool {
	return strings.HasPrefix(ns, "openshift-") || strings.HasPrefix(ns, "kube-")
}

func enrich(vm *model.VM, in Inputs) {
	k := vm.Namespace + "/" + vm.Name
	if s, ok := in.Live[k]; ok {
		applyLive(vm, s)
	}
	if in.Drift != nil { // nil = Argo not wired; non-nil = configured (absent VM is NotTracked)
		if d, ok := in.Drift[k]; ok {
			vm.Sync, vm.Health, vm.SyncError = d.Sync, d.Health, d.Message
		} else {
			vm.Sync = model.SyncNotTracked
		}
	}
}

// applyLive copies one VM's snapshot state onto its inventory row.
func applyLive(vm *model.VM, s clusterstate.LiveVM) {
	vm.Phase, vm.GuestIP, vm.NodeName = s.Phase, s.GuestIP, s.NodeName
	vm.Paused = s.Paused
	vm.IPs, vm.OS, vm.MemoryActual = s.IPs, s.OS, s.MemoryActual
	vm.VCPUs = s.VCPUs
	// Merge live per-NIC addresses onto the manifest's adapters (by name). Build a
	// fresh slice so the cached manifest VM isn't mutated across builds.
	if len(s.Interfaces) > 0 && len(vm.Networks) > 0 {
		live := make(map[string]clusterstate.LiveNIC, len(s.Interfaces))
		for _, n := range s.Interfaces {
			live[n.Name] = n
		}
		nics := make([]model.NIC, len(vm.Networks))
		copy(nics, vm.Networks)
		for i := range nics {
			if ln, ok := live[nics[i].Name]; ok {
				nics[i].MAC, nics[i].IP = ln.MAC, ln.IP
			}
		}
		vm.Networks = nics
	}
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

func toSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, it := range items {
		out[it] = struct{}{}
	}
	return out
}
