package changeset

import (
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
)

// VMsOnBranch parses every VM the branch declares - the manifest view, no live
// or Argo enrichment. Memoized on the repo per branch head, so the tree walk
// and parse run once per content change and are shared across every
// identity's inventory build; the returned slice must not be mutated.
func VMsOnBranch(read *git.Repo, branch string) ([]model.VM, error) {
	return git.Memoized(read, "vms", branch, func() ([]model.VM, error) {
		files, err := read.VMManifests(branch)
		if err != nil {
			return nil, err
		}
		var out []model.VM
		for _, f := range files {
			vms, err := manifest.ParseVMs(f.Path, f.Content, git.DefaultNamespace(f.Path))
			if err != nil {
				return nil, err
			}
			out = append(out, vms...)
		}
		return out, nil
	})
}

// findVM is the parsed VM (namespace, name) on branch, ok=false when absent -
// the desired side of drift, previews and exports.
func findVM(read *git.Repo, branch, namespace, name string) (model.VM, bool, error) {
	vms, err := VMsOnBranch(read, branch)
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
