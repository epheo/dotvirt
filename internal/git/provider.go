package git

import (
	"sort"

	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
)

// ParseVMsOnBranch parses every VM on a branch (pure manifest view, no live/argo
// enrichment). Memoized by the branch's commit hash so the tree walk + parse runs
// once per content change, shared across every identity's inventory build; the
// result is read-only to callers (the inventory builder copies each VM out). A
// content change advances the branch hash, missing the cache. The returned slice
// must not be mutated.
func (r *Repo) ParseVMsOnBranch(branch string) ([]model.VM, error) {
	hash := r.branchHash(branch)
	if hash != "" {
		r.parseMu.Lock()
		c, ok := r.parseCache[branch]
		r.parseMu.Unlock()
		if ok && c.hash == hash {
			return c.vms, nil
		}
	}

	files, err := r.VMManifests(branch)
	if err != nil {
		return nil, err
	}
	var out []model.VM
	for _, f := range files {
		vms, err := manifest.ParseVMs(f.Path, f.Content, defaultNamespace(f.Path))
		if err != nil {
			return nil, err
		}
		out = append(out, vms...)
	}

	if hash != "" {
		r.parseMu.Lock()
		r.parseCache[branch] = branchParse{hash: hash, vms: out}
		r.parseMu.Unlock()
	}
	return out, nil
}

// FindVMOnBranch returns the parsed VM (namespace, name) on a branch, ok=false if
// absent. Used for semantic diffing (staging previews, drift).
func (r *Repo) FindVMOnBranch(branch, namespace, name string) (model.VM, bool, error) {
	vms, err := r.ParseVMsOnBranch(branch)
	if err != nil {
		return model.VM{}, false, err
	}
	for _, vm := range vms {
		if vm.Namespace == namespace && vm.Name == name {
			return vm, true, nil
		}
	}
	return model.VM{}, false, nil
}

// GroupNamespaces turns a namespace→VMs map into sorted ProjectNamespace buckets
// (VMs sorted by name, namespaces by name), for the inventory builder.
func GroupNamespaces(byNS map[string][]model.VM) []model.ProjectNamespace {
	out := make([]model.ProjectNamespace, 0, len(byNS))
	for ns, vms := range byNS {
		sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
		out = append(out, model.ProjectNamespace{Namespace: ns, VMs: vms})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace < out[j].Namespace })
	return out
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
