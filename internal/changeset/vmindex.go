package changeset

import (
	"log"
	"slices"
	"strings"

	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
)

// VMsOnBranch parses every VM the branch declares - the manifest view, no live
// or Argo enrichment. Memoized on the repo per branch head, so the tree walk
// and parse run once per content change and are shared across every
// identity's inventory build; the returned slice must not be mutated.
//
// A declaring file that does not parse is logged and skipped, once per head:
// one bad manifest must not blank the project's inventory. Its VMs stay in the
// declared index, which is what keeps adoption from restating them.
func VMsOnBranch(read *git.Repo, branch string) ([]model.VM, error) {
	return git.Memoized(read, "vms", branch, func() ([]model.VM, error) {
		idx, err := indexDeclared(read, branch)
		if err != nil {
			return nil, err
		}
		files := map[string]bool{}
		for ref, path := range idx.Files {
			if ref.Kind == model.KindVM.Kind {
				files[path] = true
			}
		}
		var out []model.VM
		err = read.WalkYAML(branch,
			func(path string) bool { return files[path] },
			func(path string, content []byte) error {
				vms, err := manifest.ParseVMs(path, content, defaultNamespace(path))
				if err != nil {
					log.Printf("changeset: %s branch %s: skipping %v", read.URL(), branch, err)
					return nil
				}
				out = append(out, vms...)
				return nil
			})
		if err != nil {
			return nil, err
		}
		slices.SortStableFunc(out, func(a, b model.VM) int { return strings.Compare(a.SourceFile, b.SourceFile) })
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
