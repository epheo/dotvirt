package git

import (
	"strings"

	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
)

// unmanagedClusterScoped lists the cluster-scoped kinds a repo may carry that
// dotvirt does not manage; the managed ones answer from the kind table. A
// cluster-scoped manifest lives in a directory too, but that directory is not
// its namespace - Argo and the cluster identify these with an empty one.
var unmanagedClusterScoped = map[string]bool{
	"ClusterRole":        true,
	"ClusterRoleBinding": true,
	"StorageClass":       true,
	"PersistentVolume":   true,
	"Node":               true,
}

// ClusterScoped reports whether kind carries no namespace.
func ClusterScoped(kind string) bool {
	if _, k, ok := model.LookupKind(kind); ok {
		return k.ClusterScoped
	}
	return unmanagedClusterScoped[kind]
}

// DeclaredRefs: the objects the manifest bytes declare, any kind, multi-doc.
// path defaults the namespace (<ns>/... layout) for namespaced kinds. An
// unparsable document declares nothing; consumers only widen, and the manifest
// parsers report the syntax.
func DeclaredRefs(path string, content []byte) []model.ObjectRef {
	refs, _ := declared(path, content)
	return refs
}

// declared is manifest.Headers under the repo's namespace rule: a cluster-scoped
// kind has none, a namespaced one that omits it takes the file's directory. A
// file whose document count exceeds its refs holds content the declared index
// cannot account for.
func declared(path string, content []byte) (refs []model.ObjectRef, docs int) {
	heads, docs := manifest.Headers(content)
	for _, h := range heads {
		if ClusterScoped(h.Kind) {
			h.Namespace = ""
		} else if h.Namespace == "" {
			h.Namespace = DefaultNamespace(path)
		}
		refs = append(refs, h)
	}
	return refs, docs
}

// DefaultNamespace derives a namespace for manifests that omit metadata.namespace,
// using the manifest's top-level directory as a convention (a common GitOps
// layout: one directory per namespace). Files at the repo root fall back to
// "default".
func DefaultNamespace(path string) string {
	if dir, _, ok := strings.Cut(path, "/"); ok {
		return dir
	}
	return "default"
}

// DeclaredIndex is one branch's declared objects: which file holds each, and
// which files hold more than their declared objects (extra documents the
// index cannot name), so a rewrite or removal of those files is never offered
// as if it touched only the object asked for.
type DeclaredIndex struct {
	Files  map[model.ObjectRef]string
	Opaque map[string]bool
}

// DeclaredFilesOnBranch indexes every object the branch declares by file - the
// edit and delete paths' answer to "which file do I rewrite", regardless of
// where the manifest was committed. Git is the authority on what git describes:
// ArgoCD's tracking annotation only records what it has already applied, so it
// misses an object committed but not yet synced, one whose Application is
// broken, and every object on a cluster tracking by label instead.
//
// templates/ is excluded to match the Application's own source exclusion: a
// template is a blueprint the repo stores, not an object it declares. Memoized
// per branch head; the index is shared with later callers: read only.
func (r *Repo) DeclaredFilesOnBranch(branch string) (DeclaredIndex, error) {
	return Memoized(r, "declared", branch, func() (DeclaredIndex, error) {
		idx := DeclaredIndex{Files: map[model.ObjectRef]string{}, Opaque: map[string]bool{}}
		err := r.walkYAML(branch,
			func(path string) bool { return !inTemplatesDir(path) },
			func(path string, content []byte) error {
				refs, docs := declared(path, content)
				for _, ref := range refs {
					idx.Files[ref] = path
				}
				if docs != len(refs) {
					idx.Opaque[path] = true
				}
				return nil
			})
		if err != nil {
			return DeclaredIndex{}, err
		}
		return idx, nil
	})
}

// DeclaredOnBranch is the set of objects the branch declares - what adoption
// asks before capturing, so it never restates something the repo already holds.
func (r *Repo) DeclaredOnBranch(branch string) (map[model.ObjectRef]bool, error) {
	idx, err := r.DeclaredFilesOnBranch(branch)
	if err != nil {
		return nil, err
	}
	out := make(map[model.ObjectRef]bool, len(idx.Files))
	for ref := range idx.Files {
		out[ref] = true
	}
	return out, nil
}
