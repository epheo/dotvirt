// Package vmtemplate reads, renders, and derives template.kubevirt.io
// VirtualMachineTemplate manifests - dotvirt's content library. Templates live
// in git under templates/ (a path the ArgoCD Applications exclude, so no
// cluster-side CRD is needed); rendering runs in-process with the upstream
// engine, giving byte-identical semantics to the future in-cluster process
// subresource.
package vmtemplate

import (
	"encoding/json"
	pathpkg "path"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"

	tplv1beta1 "kubevirt.io/virt-template-api/core/v1beta1"

	"github.com/epheo/dotvirt/internal/model"
)

const (
	// APIVersion and Kind of the manifests a library holds - the native
	// virt-template schema, stored verbatim.
	APIVersion = "template.kubevirt.io/v1beta1"
	Kind       = "VirtualMachineTemplate"

	// Dir is the library directory within a repo. It must stay outside the
	// ArgoCD-applied file set (the operator's Applications exclude it) until
	// the CRD exists on-cluster.
	Dir = model.TemplatesDir

	// descriptionAnnotation carries the template's human description; the CRD
	// spec has no description field, so the convention rides metadata.
	descriptionAnnotation = "description"
)

// Path is the library file a template named name lives at.
func Path(name string) string { return Dir + "/" + name + ".yaml" }

// Parse decodes one templates/*.yaml file into a catalog entry. Parse is
// tolerant: a file that fails to decode is still listed, carrying Error, so a
// bad commit degrades one entry instead of hiding the whole library.
func Parse(path string, content []byte, library string) model.Template {
	base := pathpkg.Base(path)
	t := model.Template{
		Name:       strings.TrimSuffix(base, pathpkg.Ext(base)),
		Library:    library,
		SourceFile: path,
		YAML:       string(content),
	}

	var tpl tplv1beta1.VirtualMachineTemplate
	if err := yaml.Unmarshal(content, &tpl); err != nil {
		t.Error = "invalid template YAML: " + err.Error()
		return t
	}
	if tpl.Kind != Kind || tpl.APIVersion != APIVersion {
		t.Error = "not a " + APIVersion + " " + Kind + " manifest"
		return t
	}

	t.Description = tpl.Annotations[descriptionAnnotation]
	for _, p := range tpl.Spec.Parameters {
		t.Parameters = append(t.Parameters, model.TemplateParameter{
			Name:        p.Name,
			DisplayName: p.DisplayName,
			Description: p.Description,
			Value:       p.Value,
			Generate:    p.Generate,
			From:        p.From,
			Required:    p.Required,
		})
	}

	// Blueprint summary for the catalog row; parameterized or absent values
	// simply stay empty.
	if vm := tpl.Spec.VirtualMachine; vm != nil && len(vm.Raw) > 0 {
		var bp struct {
			Spec struct {
				Instancetype struct {
					Name string `json:"name"`
				} `json:"instancetype"`
				Preference struct {
					Name string `json:"name"`
				} `json:"preference"`
				DataVolumeTemplates []struct {
					Spec struct {
						SourceRef *struct {
							Kind      string `json:"kind"`
							Name      string `json:"name"`
							Namespace string `json:"namespace"`
						} `json:"sourceRef"`
					} `json:"spec"`
				} `json:"dataVolumeTemplates"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(vm.Raw, &bp); err == nil {
			t.Instancetype = bp.Spec.Instancetype.Name
			t.Preference = bp.Spec.Preference.Name
			for _, dv := range bp.Spec.DataVolumeTemplates {
				if ref := dv.Spec.SourceRef; ref != nil && (ref.Kind == "" || ref.Kind == "DataSource") {
					t.Image = blueprintImage(ref.Namespace, ref.Name, t.Parameters)
					break
				}
			}
		}
	}
	return t
}

// blueprintImage resolves a blueprint's DataSource reference with the
// parameter defaults a deploy would apply. A reference left to a parameter
// without a default, or without a namespace, names no image the catalog can
// match.
func blueprintImage(namespace, name string, params []model.TemplateParameter) string {
	defaults := map[string]string{}
	for _, p := range params {
		if p.Value != "" {
			defaults[p.Name] = p.Value
		}
	}
	resolved := true
	expand := func(s string) string {
		return paramRef.ReplaceAllStringFunc(s, func(m string) string {
			v, ok := defaults[paramRef.FindStringSubmatch(m)[1]]
			if !ok {
				resolved = false
			}
			return v
		})
	}
	namespace, name = expand(namespace), expand(name)
	if !resolved || namespace == "" || name == "" {
		return ""
	}
	return namespace + "/" + name
}

var paramRef = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)
