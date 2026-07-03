package vmtemplate

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/model"
)

// Derive builds a VirtualMachineTemplate from an existing VM's git manifest
// ("Clone to Template"): the VM becomes the blueprint with its identity
// parameterized — metadata.name turns into ${NAME} backed by a
// generate-expression default derived from the source name. DataVolume names
// prefixed with the VM's name are re-anchored to ${NAME} too: DataVolumes are
// namespace-unique, so a second deploy of the template must not collide with
// the first. Everything else (disks, networks, cloud-init) carries verbatim;
// parameterizing further fields is a template-editing concern.
func Derive(vmYAML []byte, name, description string) ([]byte, error) {
	var vm map[string]any
	if err := yaml.Unmarshal(vmYAML, &vm); err != nil {
		return nil, fmt.Errorf("%w: VM manifest: %v", model.ErrInvalid, err)
	}
	if vm["kind"] != "VirtualMachine" {
		return nil, fmt.Errorf("%w: source is not a VirtualMachine manifest", model.ErrInvalid)
	}
	meta, _ := vm["metadata"].(map[string]any)
	if meta == nil {
		return nil, fmt.Errorf("%w: VM manifest has no metadata", model.ErrInvalid)
	}
	base, _ := meta["name"].(string)
	if base == "" {
		return nil, fmt.Errorf("%w: VM manifest has no name", model.ErrInvalid)
	}
	delete(meta, "namespace")
	delete(vm, "status")
	meta["name"] = "${NAME}"
	reanchorDataVolumes(vm, base)

	tplMeta := map[string]any{"name": name}
	if description != "" {
		tplMeta["annotations"] = map[string]any{descriptionAnnotation: description}
	}
	tpl := map[string]any{
		"apiVersion": APIVersion,
		"kind":       Kind,
		"metadata":   tplMeta,
		"spec": map[string]any{
			"parameters": []any{map[string]any{
				"name":        "NAME",
				"description": "Unique VM name",
				"generate":    "expression",
				"from":        namePattern(base),
			}},
			"virtualMachine": vm,
		},
	}
	return yaml.Marshal(tpl)
}

// reanchorDataVolumes rewrites "<vmname>*" DataVolume names to "${NAME}*" in
// spec.dataVolumeTemplates and the volumes that mount them, keeping the two
// sides consistent.
func reanchorDataVolumes(vm map[string]any, vmName string) {
	spec, _ := vm["spec"].(map[string]any)
	if spec == nil {
		return
	}
	rename := func(m map[string]any, key string) {
		if n, _ := m[key].(string); strings.HasPrefix(n, vmName) {
			m[key] = "${NAME}" + strings.TrimPrefix(n, vmName)
		}
	}
	if dvts, _ := spec["dataVolumeTemplates"].([]any); dvts != nil {
		for _, e := range dvts {
			if dvt, ok := e.(map[string]any); ok {
				if md, ok := dvt["metadata"].(map[string]any); ok {
					rename(md, "name")
				}
			}
		}
	}
	tmpl, _ := spec["template"].(map[string]any)
	if tmpl == nil {
		return
	}
	tspec, _ := tmpl["spec"].(map[string]any)
	if tspec == nil {
		return
	}
	if vols, _ := tspec["volumes"].([]any); vols != nil {
		for _, e := range vols {
			vol, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if dv, ok := vol["dataVolume"].(map[string]any); ok {
				rename(dv, "name")
			}
		}
	}
}

// namePattern builds the generated-name expression "<base>-[a-z0-9]{5}",
// trimming base so generated names stay within the 63-char DNS-label limit.
func namePattern(base string) string {
	const suffix = 6 // "-" plus 5 generated characters
	if len(base) > 63-suffix {
		base = base[:63-suffix]
	}
	base = strings.TrimRight(base, "-")
	return base + "-[a-z0-9]{5}"
}
