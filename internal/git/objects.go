package git

import (
	"sync"

	"github.com/epheo/dotvirt/internal/model"
)

// branchDecl is one branch's declared-object index and the commit it reflects.
type branchDecl struct {
	hash  string
	files map[model.ObjectRef]string
}

// declIndex memoizes DeclaredFilesOnBranch the way parseCache memoizes the VM
// parse: keyed by branch, invalidated when the branch hash moves.
type declIndex struct {
	mu    sync.Mutex
	cache map[string]branchDecl
}

// DeclaredFilesOnBranch maps every object the branch declares to the file holding
// it - the edit and delete paths' answer to "which file do I rewrite", regardless
// of where the manifest was committed. Same exclusions as DeclaredOnBranch. The
// map is shared with later callers: read only.
func (r *Repo) DeclaredFilesOnBranch(branch string) (map[model.ObjectRef]string, error) {
	hash := r.branchHash(branch)
	r.decl.mu.Lock()
	c, ok := r.decl.cache[branch]
	r.decl.mu.Unlock()
	if ok && hash != "" && c.hash == hash {
		return c.files, nil
	}
	files := map[model.ObjectRef]string{}
	err := r.walkYAML(branch,
		func(path string) bool { return !inTemplatesDir(path) },
		func(path string, content []byte) error {
			for _, ref := range DeclaredRefs(path, content) {
				files[ref] = path
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	if hash != "" {
		r.decl.mu.Lock()
		if r.decl.cache == nil {
			r.decl.cache = map[string]branchDecl{}
		}
		r.decl.cache[branch] = branchDecl{hash: hash, files: files}
		r.decl.mu.Unlock()
	}
	return files, nil
}
