package git

import (
	"sync"

	"github.com/epheo/dotvirt/internal/model"
)

// DeclaredIndex is one branch's declared objects: which file holds each, and
// which files hold more than their declared objects (extra documents the
// index cannot name), so a rewrite or removal of those files is never offered
// as if it touched only the object asked for.
type DeclaredIndex struct {
	Files  map[model.ObjectRef]string
	Opaque map[string]bool
}

// branchDecl is one branch's index and the commit it reflects.
type branchDecl struct {
	hash  string
	index DeclaredIndex
}

// declIndex memoizes DeclaredFilesOnBranch the way parseCache memoizes the VM
// parse: keyed by branch, invalidated when the branch hash moves.
type declIndex struct {
	mu    sync.Mutex
	cache map[string]branchDecl
}

// DeclaredFilesOnBranch indexes every object the branch declares by file - the
// edit and delete paths' answer to "which file do I rewrite", regardless of
// where the manifest was committed. Same exclusions as DeclaredOnBranch. The
// index is shared with later callers: read only.
func (r *Repo) DeclaredFilesOnBranch(branch string) (DeclaredIndex, error) {
	hash := r.branchHash(branch)
	r.decl.mu.Lock()
	c, ok := r.decl.cache[branch]
	r.decl.mu.Unlock()
	if ok && hash != "" && c.hash == hash {
		return c.index, nil
	}
	idx := DeclaredIndex{Files: map[model.ObjectRef]string{}, Opaque: map[string]bool{}}
	err := r.walkYAML(branch,
		func(path string) bool { return !inTemplatesDir(path) },
		func(path string, content []byte) error {
			refs := DeclaredRefs(path, content)
			for _, ref := range refs {
				idx.Files[ref] = path
			}
			if Documents(content) != len(refs) {
				idx.Opaque[path] = true
			}
			return nil
		})
	if err != nil {
		return DeclaredIndex{}, err
	}
	if hash != "" {
		r.decl.mu.Lock()
		if r.decl.cache == nil {
			r.decl.cache = map[string]branchDecl{}
		}
		r.decl.cache[branch] = branchDecl{hash: hash, index: idx}
		r.decl.mu.Unlock()
	}
	return idx, nil
}
