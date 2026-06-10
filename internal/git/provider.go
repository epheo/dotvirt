package git

import (
	"sort"

	"github.com/epheo/dotvirt/internal/model"
)

// LiveState returns actual cluster state keyed by "namespace/name", used to
// enrich the git-derived inventory. Implemented by the cluster plane.
type LiveState struct {
	Phase    string
	GuestIP  string
	NodeName string
}

// Enricher supplies live cluster state for the inventory. Optional.
type Enricher func() (map[string]LiveState, error)

// Drift is a VM's ArgoCD sync/health, keyed by "namespace/name". Implemented by
// the argo plane.
type Drift struct {
	Sync   model.SyncStatus
	Health string
}

// DriftSource supplies per-VM ArgoCD drift for the inventory. Optional.
type DriftSource func() (map[string]Drift, error)

// Provider adapts a Repo to the inventory API: it lists branches and builds the
// per-branch project→VM tree from the manifests, optionally enriched with live
// cluster state and ArgoCD drift.
type Provider struct {
	repo   *Repo
	enrich Enricher
	drift  DriftSource
}

// NewProvider wraps a Repo as an inventory provider.
func NewProvider(repo *Repo) *Provider { return &Provider{repo: repo} }

// WithEnricher attaches a live-state source; the returned provider enriches each
// VM with phase/IP/node when building inventory.
func (p *Provider) WithEnricher(e Enricher) *Provider {
	p.enrich = e
	return p
}

// WithDrift attaches an ArgoCD drift source; VMs get Synced/OutOfSync from Argo,
// or NotTracked when no Application manages them.
func (p *Provider) WithDrift(d DriftSource) *Provider {
	p.drift = d
	return p
}

// Branches lists the repo's branches.
func (p *Provider) Branches() ([]string, error) {
	if err := p.repo.Fetch(); err != nil {
		return nil, err
	}
	return p.repo.Branches()
}

// FindVM parses the VM (namespace, name) as it appears on a branch, for semantic
// diffing (staging previews, drift). Returns ok=false if not present. It does
// NOT enrich with live/argo state — it's the pure manifest view. Fetches first
// so it reflects the latest remote (e.g. dotvirt's running-branch exports).
func (p *Provider) FindVM(branch, namespace, name string) (model.VM, bool, error) {
	if err := p.repo.Fetch(); err != nil {
		return model.VM{}, false, err
	}
	files, err := p.repo.VMManifests(branch)
	if err != nil {
		return model.VM{}, false, err
	}
	for _, f := range files {
		vms, err := ParseVMs(f.Path, f.Content, defaultNamespace(f.Path))
		if err != nil {
			return model.VM{}, false, err
		}
		for _, vm := range vms {
			if vm.Namespace == namespace && vm.Name == name {
				return vm, true, nil
			}
		}
	}
	return model.VM{}, false, nil
}

// Inventory builds the inventory tree for a branch. An empty branch defaults to
// the first available branch so the UI has something to show on first load.
// It fetches first so the read reflects the latest remote state — including
// dotvirt's own running-branch exports.
func (p *Provider) Inventory(branch string) (any, error) {
	if err := p.repo.Fetch(); err != nil {
		return nil, err
	}
	if branch == "" {
		branches, err := p.repo.Branches()
		if err != nil {
			return nil, err
		}
		if len(branches) == 0 {
			return model.Inventory{Branch: "", Projects: []model.Project{}}, nil
		}
		branch = branches[0]
	}

	files, err := p.repo.VMManifests(branch)
	if err != nil {
		return nil, err
	}

	var live map[string]LiveState
	if p.enrich != nil {
		if live, err = p.enrich(); err != nil {
			return nil, err
		}
	}

	var drift map[string]Drift
	if p.drift != nil {
		if drift, err = p.drift(); err != nil {
			return nil, err
		}
	}

	byNS := map[string][]model.VM{}
	for _, f := range files {
		vms, err := ParseVMs(f.Path, f.Content, defaultNamespace(f.Path))
		if err != nil {
			return nil, err
		}
		for _, vm := range vms {
			k := vm.Namespace + "/" + vm.Name
			if s, ok := live[k]; ok {
				vm.Phase, vm.GuestIP, vm.NodeName = s.Phase, s.GuestIP, s.NodeName
			}
			if p.drift != nil {
				if d, ok := drift[k]; ok {
					vm.Sync, vm.Health = d.Sync, d.Health
				} else {
					vm.Sync = model.SyncNotTracked // drift enabled but no Application manages this VM
				}
			}
			byNS[vm.Namespace] = append(byNS[vm.Namespace], vm)
		}
	}

	return model.Inventory{Branch: branch, Projects: groupProjects(byNS)}, nil
}

func groupProjects(byNS map[string][]model.VM) []model.Project {
	projects := make([]model.Project, 0, len(byNS))
	for ns, vms := range byNS {
		sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
		projects = append(projects, model.Project{Namespace: ns, VMs: vms})
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Namespace < projects[j].Namespace })
	return projects
}

// defaultNamespace derives a namespace for manifests that omit metadata.namespace,
// using the manifest's top-level directory as a convention (a common GitOps
// layout: one directory per namespace). Files at the repo root fall back to
// "default".
func defaultNamespace(path string) string {
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "default"
}
