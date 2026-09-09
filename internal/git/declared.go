package git

import (
	"bytes"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/epheo/dotvirt/internal/model"
)

// declaredDoc is the identity header every Kubernetes manifest carries; the rest of
// each document is ignored, so this reads any kind without a typed model.
type declaredDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
}

// clusterScoped lists the kinds dotvirt's repos carry that have no namespace:
// the platform tier's tenancy and network objects. A cluster-scoped manifest
// lives in a directory too, but that directory is not its namespace - Argo and
// the cluster identify these with an empty one.
var clusterScoped = map[string]bool{
	"Namespace":                      true,
	"ClusterUserDefinedNetwork":      true,
	"NodeNetworkConfigurationPolicy": true,
	"AdminNetworkPolicy":             true,
	"BaselineAdminNetworkPolicy":     true,
	"EgressIP":                       true,
	"AdminPolicyBasedExternalRoute":  true,
	"ClusterRole":                    true,
	"ClusterRoleBinding":             true,
	"StorageClass":                   true,
	"PersistentVolume":               true,
	"Node":                           true,
}

// ClusterScoped reports whether kind carries no namespace.
func ClusterScoped(kind string) bool { return clusterScoped[kind] }

// DeclaredRefs: the objects the manifest bytes declare, any kind, multi-doc.
// path defaults the namespace (<ns>/... layout) for namespaced kinds. An
// unparsable document declares nothing; consumers only widen, and the manifest
// parsers report the syntax.
func DeclaredRefs(path string, content []byte) []model.ObjectRef {
	var out []model.ObjectRef
	dec := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var doc declaredDoc
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if doc.Kind == "" || doc.Metadata.Name == "" {
			continue
		}
		ns := doc.Metadata.Namespace
		if ClusterScoped(doc.Kind) {
			ns = ""
		} else if ns == "" {
			ns = DefaultNamespace(path)
		}
		out = append(out, model.ObjectRef{Kind: doc.Kind, Namespace: ns, Name: doc.Metadata.Name})
	}
	return out
}

// Documents counts the YAML documents in content, every one of them: empty,
// comment-only and unparsable documents count too (an unparsable tail ends
// the count at -1). DeclaredRefs skips what it cannot name, so a file whose
// count exceeds its refs holds content the declared index cannot account for.
func Documents(content []byte) int {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	n := 0
	for {
		var doc any
		err := dec.Decode(&doc)
		if err == io.EOF {
			return n
		}
		if err != nil {
			return -1
		}
		n++
	}
}

// DeclaredOnBranch returns every object the branch declares. Git is the authority on
// what git describes: ArgoCD's tracking annotation only records what it has already
// applied, so it misses an object committed but not yet synced, one whose Application
// is broken, and every object on a cluster tracking by label instead. Adoption asks
// this before capturing, so it never restates something the repo already holds.
//
// templates/ is excluded to match the Application's own source exclusion: a template
// is a blueprint the repo stores, not an object it declares.
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
