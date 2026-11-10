package manifest

import (
	"bytes"
	"io"

	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/model"
)

// header is the identity every Kubernetes manifest document carries; the rest
// of the document is left undecoded, so any kind reads without a typed model.
type header struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   objectMeta `yaml:"metadata"`
}

type objectMeta struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

func (h header) ref() model.ObjectRef {
	return model.ObjectRef{Kind: h.Kind, Namespace: h.Metadata.Namespace, Name: h.Metadata.Name}
}

// Header is the first document's kind, name and namespace as written: the
// namespace stays empty when the document omits it, whatever the kind's scope.
func Header(content []byte) (model.ObjectRef, error) {
	var h header
	if err := yaml.Unmarshal(content, &h); err != nil {
		return model.ObjectRef{}, err
	}
	return h.ref(), nil
}

// Headers is Header over every document of a file, in one pass: the
// identities declared, in order, and the document count - headerless
// documents counted too, so a count above len(heads) means the file holds
// content the headers cannot name. A null document (a trailing ---, a
// comment-only one) is no content, as kubectl reads it, so it is not counted.
// A document without kind or name declares nothing; docs is -1 when a
// document fails to parse.
func Headers(content []byte) (heads []model.ObjectRef, docs int) {
	docs = eachHeader(content, func(h header) {
		if h.Kind != "" && h.Metadata.Name != "" {
			heads = append(heads, h.ref())
		}
	})
	return heads, docs
}

// Doc is one document's identity with its API group, which ObjectRef leaves
// out: ObjectRef keys the declared index, where a kind's group never varies.
type Doc struct {
	model.ObjectRef
	Group string
}

// Docs is Headers with each identity's API group, from the same walk.
func Docs(content []byte) (out []Doc, docs int) {
	docs = eachHeader(content, func(h header) {
		if h.Kind != "" && h.Metadata.Name != "" {
			gv, _ := schema.ParseGroupVersion(h.APIVersion)
			out = append(out, Doc{ObjectRef: h.ref(), Group: gv.Group})
		}
	})
	return out, docs
}

// Kinds is the group and kind of every document of a file naming both an
// apiVersion and a kind, in order, through the first that fails to parse.
func Kinds(content []byte) []schema.GroupKind {
	var out []schema.GroupKind
	eachHeader(content, func(h header) {
		gv, err := schema.ParseGroupVersion(h.APIVersion)
		if h.APIVersion != "" && h.Kind != "" && err == nil {
			out = append(out, schema.GroupKind{Group: gv.Group, Kind: h.Kind})
		}
	})
	return out
}

// eachHeader visits the header of every non-null document of content, in
// order: the count Headers reports, -1 once one fails to parse.
func eachHeader(content []byte, visit func(header)) (docs int) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if err == io.EOF {
			return docs
		}
		if err != nil {
			return -1
		}
		if len(doc.Content) == 0 || doc.Content[0].ShortTag() == "!!null" {
			continue
		}
		docs++
		var h header
		if err := doc.Decode(&h); err != nil {
			return -1
		}
		visit(h)
	}
}
