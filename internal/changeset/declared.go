package changeset

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/tenancy"
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
	objs, _, _ := declared(path, content)
	refs := make([]model.ObjectRef, len(objs))
	for i, o := range objs {
		refs[i] = o.ObjectRef
	}
	return refs
}

// GroupRef is a declared object with its API group, which ObjectRef leaves out
// because it keys the declared index: a tenant repo's kind rule reads both.
type GroupRef struct {
	model.ObjectRef
	Group string
}

// declared is manifest.Docs under the repo's namespace rule: a cluster-scoped
// kind has none, a namespaced one that omits it takes the file's directory. A
// file whose document count exceeds its objects holds content the declared
// index cannot account for. implied are the refs whose namespace came from the
// directory: Argo places those in its Application's destination namespace
// instead, so the two readings can disagree.
func declared(path string, content []byte) (objs []GroupRef, implied []model.ObjectRef, docs int) {
	ds, docs := manifest.Docs(content)
	for _, d := range ds {
		if clusterScoped(d.Kind) {
			d.Namespace = ""
		} else if d.Namespace == "" {
			d.Namespace = defaultNamespace(path)
			implied = append(implied, d.ObjectRef)
		}
		objs = append(objs, GroupRef{ObjectRef: d.ObjectRef, Group: d.Group})
	}
	return objs, implied, docs
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
// as if it touched only the object asked for.
type declaredIndex struct {
	Files     map[model.ObjectRef]string
	Opaque    map[string]bool
	Placement Placement
	// Repeated lists every file of an object more than one file declares;
	// Files keeps the last of them.
	Repeated map[model.ObjectRef][]string
	// Kinds is every kind a document declares, named or not, in sorted order.
	Kinds []schema.GroupKind
}

// Placement is where a branch's objects are declared, each list in tree order:
// Implied omit metadata.namespace (each under the namespace its directory gives
// it), Named name theirs, keyed by it, Namespaces holding the keys in name
// order. Declared is every object with its group, for the install's kind rule
// to judge where it is read (Undeclarable): the rule is the process's, the
// memo the branch's.
type Placement struct {
	Implied    []model.ObjectRef
	Named      map[string][]model.ObjectRef
	Namespaces []string
	Declared   []GroupRef
}

// Undeclarable are the objects a tenant repo declares that rule refuses: its
// AppProject refuses them too, which fails the project's whole sync.
func (p Placement) Undeclarable(rule KindRule) []model.ObjectRef {
	var out []model.ObjectRef
	for _, o := range p.Declared {
		if !rule.Declarable(o.Group, o.Kind) {
			out = append(out, o.ObjectRef)
		}
	}
	return out
}

// KindRule is what a tenant repo may declare on this install: the policy the
// operator put on the tenant AppProjects, and the scope of a kind the table
// does not know (ClusterScoped, from discovery; nil reads it as namespaced).
// The AppProject is the enforcement; the rule only keeps dotvirt from staging
// what it would refuse.
type KindRule struct {
	Policy        tenancy.Policy
	ClusterScoped func(group, kind string) bool
}

// Declarable reports whether a tenant repo may declare group/kind.
func (r KindRule) Declarable(group, kind string) bool {
	if !r.Policy.Any {
		return tenancy.TenantKind(kind)
	}
	return r.Policy.Declarable(group, kind, r.clusterScoped(group, kind))
}

func (r KindRule) clusterScoped(group, kind string) bool {
	if _, k, ok := model.LookupKind(kind); ok && k.Group == group {
		return k.ClusterScoped
	}
	return unmanagedClusterScoped[kind] || r.ClusterScoped != nil && r.ClusterScoped(group, kind)
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
		idx := declaredIndex{Files: map[model.ObjectRef]string{}, Opaque: map[string]bool{}, Repeated: map[model.ObjectRef][]string{}}
		named := map[string][]model.ObjectRef{}
		kinds := map[schema.GroupKind]bool{}
		err := read.WalkYAML(branch,
			func(path string) bool { return !git.InTemplatesDir(path) },
			func(path string, content []byte) error {
				for _, k := range manifest.Kinds(content) {
					kinds[k] = true
				}
				objs, implied, docs := declared(path, content)
				for _, o := range objs {
					ref := o.ObjectRef
					if prev, ok := idx.Files[ref]; ok && prev != path {
						if len(idx.Repeated[ref]) == 0 {
							idx.Repeated[ref] = []string{prev}
						}
						idx.Repeated[ref] = append(idx.Repeated[ref], path)
					}
					idx.Files[ref] = path
					if ref.Namespace != "" && !slices.Contains(implied, ref) {
						named[ref.Namespace] = append(named[ref.Namespace], ref)
					}
				}
				idx.Placement.Declared = append(idx.Placement.Declared, objs...)
				if docs != len(objs) {
					idx.Opaque[path] = true
				}
				idx.Placement.Implied = append(idx.Placement.Implied, implied...)
				return nil
			})
		if err != nil {
			return declaredIndex{}, err
		}
		idx.Placement.Named = named
		idx.Placement.Namespaces = slices.Sorted(maps.Keys(named))
		idx.Kinds = slices.SortedFunc(maps.Keys(kinds), func(a, b schema.GroupKind) int {
			return cmp.Or(strings.Compare(a.Group, b.Group), strings.Compare(a.Kind, b.Kind))
		})
		return idx, nil
	})
}

// PlacementOnBranch is where branch declares its objects. A tenant
// Application applies a document without metadata.namespace to its destination
// namespace, and refuses its whole sync over one naming a namespace outside its
// project or of a platform-tier kind, so in a tenant repo each is a standing
// issue to fix in git. Shares the memoized declared index; read only.
func PlacementOnBranch(read *git.Repo, branch string) (Placement, error) {
	idx, err := indexDeclared(read, branch)
	if err != nil {
		return Placement{}, err
	}
	return idx.Placement, nil
}

// requirePlacement refuses content proj's repo must not declare: a namespaced
// document without metadata.namespace, which dotvirt reads in its directory's
// namespace while Argo applies it to the Application's destination; and, in a
// tenant repo, a kind rule refuses, which the project's AppProject refuses too
// because Argo would apply it with more rights than the project holds.
func requirePlacement(rule KindRule, proj project.ProjectInfo, path string, content []byte) error {
	objs, implied, _ := declared(path, content)
	if len(implied) > 0 {
		r := implied[0]
		return fmt.Errorf("%w: %s %s must set metadata.namespace", model.ErrInvalid, r.Kind, r.Name)
	}
	for _, o := range objs {
		if err := tenantDeclarable(rule, proj, o.Group, o.Kind, o.Name); err != nil {
			return err
		}
	}
	return nil
}

// tenantDeclarable refuses a kind a tenant repo may not declare, saying why:
// under the listed kinds it belongs in the platform repo; under any kind it is
// one this install's tenant repos may not apply.
func tenantDeclarable(rule KindRule, proj project.ProjectInfo, group, kind, name string) error {
	if proj.Name == project.PlatformName || rule.Declarable(group, kind) {
		return nil
	}
	if rule.Policy.Any {
		return fmt.Errorf("%w: %s %s is a kind this install's tenant repos may not apply, so project %s may not declare it",
			model.ErrInvalid, kind, name, proj.Name)
	}
	return fmt.Errorf("%w: only the platform repo may declare %s %s, not project %s",
		model.ErrInvalid, kind, name, proj.Name)
}
