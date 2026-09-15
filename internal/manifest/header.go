package manifest

import (
	"bytes"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/epheo/dotvirt/internal/model"
)

// header is the identity every Kubernetes manifest document carries; the rest
// of the document is left undecoded, so any kind reads without a typed model.
type header struct {
	Kind     string     `yaml:"kind"`
	Metadata objectMeta `yaml:"metadata"`
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
// identities declared, in order, and the document count - empty, comment-only
// and headerless documents counted too, so a count above len(heads) means the
// file holds content the headers cannot name. A document without kind or name
// declares nothing; docs is -1 when a document fails to parse.
func Headers(content []byte) (heads []model.ObjectRef, docs int) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var h header
		err := dec.Decode(&h)
		if err == io.EOF {
			return heads, docs
		}
		if err != nil {
			return heads, -1
		}
		docs++
		if h.Kind == "" || h.Metadata.Name == "" {
			continue
		}
		heads = append(heads, h.ref())
	}
}
