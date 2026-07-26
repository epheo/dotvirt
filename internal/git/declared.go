package git

import (
	"bytes"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing/object"
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

// DeclaredRefs returns the objects the manifest bytes declare, any kind, any number
// of documents. path supplies the namespace default for a manifest that omits it
// (the repo layout's <ns>/... convention). A document that does not parse declares
// nothing this can name: consumers only widen as a result, and the manifest parsers
// report the syntax error.
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
		if ns == "" {
			ns = defaultNamespace(path)
		}
		out = append(out, model.ObjectRef{Kind: doc.Kind, Namespace: ns, Name: doc.Metadata.Name})
	}
	return out
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
	r.mu.Lock()
	defer r.mu.Unlock()

	tree, err := r.treeFor(branch)
	if err != nil {
		return nil, err
	}
	out := map[model.ObjectRef]bool{}
	err = tree.Files().ForEach(func(f *object.File) error {
		if !isYAML(f.Name) || inTemplatesDir(f.Name) {
			return nil
		}
		content, err := readFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f.Name, err)
		}
		for _, ref := range DeclaredRefs(f.Name, content) {
			out[ref] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
