package changeset

import (
	"strings"

	"github.com/epheo/dotvirt/internal/git"
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

// clusterScoped reports whether kind carries no namespace.
func clusterScoped(kind string) bool {
	if _, k, ok := model.LookupKind(kind); ok {
		return k.ClusterScoped
	}
	return unmanagedClusterScoped[kind]
}

// declaredRefs: the objects the manifest bytes declare, any kind, multi-doc.
// path defaults the namespace (<ns>/... layout) for namespaced kinds. An
// unparsable document declares nothing; consumers only widen, and the manifest
// parsers report the syntax.
func declaredRefs(path string, content []byte) []model.ObjectRef {
	refs, _, _ := declared(path, content)
	return refs
}

// declared is manifest.Headers under the repo's namespace rule: a cluster-scoped
// kind has none, a namespaced one that omits it takes the file's directory. A
// file whose document count exceeds its refs holds content the declared index
// cannot account for. implied are the refs whose namespace came from the
// directory: Argo places those in its Application's destination namespace
// instead, so the two readings can disagree.
func declared(path string, content []byte) (refs, implied []model.ObjectRef, docs int) {
	heads, docs := manifest.Headers(content)
	for _, h := range heads {
		if clusterScoped(h.Kind) {
			h.Namespace = ""
		} else if h.Namespace == "" {
			h.Namespace = defaultNamespace(path)
			implied = append(implied, h)
		}
		refs = append(refs, h)
	}
	return refs, implied, docs
}

// defaultNamespace derives a namespace for manifests that omit metadata.namespace,
// using the manifest's top-level directory as a convention (a common GitOps
// layout: one directory per namespace). Files at the repo root fall back to
// "default".
func defaultNamespace(path string) string {
	if dir, _, ok := strings.Cut(path, "/"); ok {
		return dir
	}
	return "default"
}

// declaredIndex is one branch's declared objects: which file holds each, and
// which files hold more than their declared objects (extra documents the
// index cannot name), so a rewrite or removal of those files is never offered
// as if it touched only the object asked for. Implied lists, in tree order,
// the namespaced objects whose document omits metadata.namespace.
type declaredIndex struct {
	Files   map[model.ObjectRef]string
	Opaque  map[string]bool
	Implied []model.ObjectRef
}

// indexDeclared indexes every object branch declares by file - the edit and
// delete paths' answer to "which file do I rewrite", regardless of where the
// manifest was committed. Git is the authority on what git describes: ArgoCD's
// tracking annotation only records what it has already applied, so it misses
// an object committed but not yet synced, one whose Application is broken, and
// every object on a cluster tracking by label instead.
//
// templates/ is excluded to match the Application's own source exclusion: a
// template is a blueprint the repo stores, not an object it declares. Memoized
// per branch head; the index is shared with later callers: read only.
func indexDeclared(read *git.Repo, branch string) (declaredIndex, error) {
	return git.Memoized(read, "declared", branch, func() (declaredIndex, error) {
		idx := declaredIndex{Files: map[model.ObjectRef]string{}, Opaque: map[string]bool{}}
		err := read.WalkYAML(branch,
			func(path string) bool { return !git.InTemplatesDir(path) },
			func(path string, content []byte) error {
				refs, implied, docs := declared(path, content)
				for _, ref := range refs {
					idx.Files[ref] = path
				}
				if docs != len(refs) {
					idx.Opaque[path] = true
				}
				idx.Implied = append(idx.Implied, implied...)
				return nil
			})
		if err != nil {
			return declaredIndex{}, err
		}
		return idx, nil
	})
}

// UnnamespacedOnBranch lists the namespaced objects branch declares without
// metadata.namespace, each under the namespace dotvirt reads from its
// directory. Argo applies them to the Application's destination namespace, so
// such a document is a standing issue to fix in git. Shares the memoized
// declared index; read only.
func UnnamespacedOnBranch(read *git.Repo, branch string) ([]model.ObjectRef, error) {
	idx, err := indexDeclared(read, branch)
	if err != nil {
		return nil, err
	}
	return idx.Implied, nil
}
