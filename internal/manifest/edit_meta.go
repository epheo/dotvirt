package manifest

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// applyMetadata upserts/removes labels under metadata.
func applyMetadata(ed *lineEditor, vmRoot *yaml.Node, edit VMEdit) {
	if len(edit.SetLabels) == 0 && len(edit.RemoveLabels) == 0 {
		return
	}
	meta := get(vmRoot, "metadata")
	if meta == nil {
		return
	}
	applyMapEdits(ed, meta, "labels", edit.SetLabels, edit.RemoveLabels)
}

// applyMapEdits sets and removes keys within a string-map field (e.g. labels) of
// a parent mapping, creating the field if a set is requested and it's absent.
func applyMapEdits(ed *lineEditor, parent *yaml.Node, field string, set map[string]string, remove []string) {
	if len(set) == 0 && len(remove) == 0 {
		return
	}
	m := mappingField(ed, parent, field)

	if m == nil {
		if len(set) == 0 {
			return // nothing to remove from a non-existent map
		}
		// Create the field with the set entries, in sorted key order so the
		// written YAML is deterministic and matches the (sorted) preview.
		block := []string{field + ":"}
		for _, k := range sortedKeys(set) {
			block = append(block, "  "+quoteKey(k)+": "+set[k])
		}
		ed.insertBlock(parent, block)
		return
	}

	// If the edit empties the map, drop the whole field: a dangling `field:`
	// key with a null value is the degenerate shape mappingField guards
	// against — never leave one behind.
	if len(set) == 0 && emptiesMap(m, remove) {
		ed.deleteChild(parent, field)
		return
	}

	for _, k := range sortedKeys(set) {
		if existing := get(m, k); existing != nil {
			ed.setScalarAt(existing, set[k])
		} else {
			ed.insertChild(m, quoteKey(k), set[k])
		}
	}
	for _, k := range remove {
		if existing := get(m, k); existing != nil {
			ed.removeLine(existing.Line - 1) // key/value share a line for scalars
		}
	}
}

// mappingField returns parent's field as an extendable block mapping. A
// degenerate value — null (e.g. a map an earlier edit fully emptied) or an
// empty {} — has no child line to anchor an insert on, so it is deleted here
// and nil returned: the caller recreates the field wholesale.
func mappingField(ed *lineEditor, parent *yaml.Node, field string) *yaml.Node {
	m := get(parent, field)
	if m == nil {
		return nil
	}
	if m.Kind != yaml.MappingNode || len(m.Content) == 0 {
		ed.deleteChild(parent, field)
		return nil
	}
	return m
}

// emptiesMap reports whether removing the given keys leaves the map empty.
func emptiesMap(m *yaml.Node, remove []string) bool {
	removed := make(map[string]bool, len(remove))
	for _, k := range remove {
		removed[k] = true
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !removed[m.Content[i].Value] {
			return false
		}
	}
	return true
}

// quoteKey quotes a map key if it contains characters (like '/') that need it.
func quoteKey(k string) string {
	for _, r := range k {
		if r == '/' || r == '.' || r == ':' {
			return fmt.Sprintf("%q", k)
		}
	}
	return k
}

// PreferNoEvictionAnnotation is the descheduler's per-VM opt-out: its PRESENCE
// (the value is not evaluated) makes the automatic load balancer skip the VM,
// while a node drain still live-migrates it. Set on the VM TEMPLATE metadata so
// KubeVirt propagates it to the virt-launcher pod the descheduler inspects.
const PreferNoEvictionAnnotation = "descheduler.alpha.kubernetes.io/prefer-no-eviction"

// applyTemplateAnnotations upserts/removes annotations under
// spec.template.metadata — today only the DRS-exclude toggle.
func applyTemplateAnnotations(ed *lineEditor, vmRoot *yaml.Node, edit VMEdit) {
	if edit.DRSExclude == nil {
		return
	}
	// The value must stay a YAML string ("true" bare would parse as a bool,
	// which the API rejects for annotations), so it is spliced pre-quoted.
	set, remove := map[string]string{PreferNoEvictionAnnotation: `"true"`}, []string(nil)
	if !*edit.DRSExclude {
		set, remove = nil, []string{PreferNoEvictionAnnotation}
	}
	tmpl := get(get(vmRoot, "spec"), "template")
	if tmpl == nil {
		return
	}
	// mappingField, not get: an empty `metadata: {}` must be recreated as a
	// block, the same degenerate-shape rule applyMapEdits applies one level down.
	if meta := mappingField(ed, tmpl, "metadata"); meta != nil {
		applyMapEdits(ed, meta, "annotations", set, remove)
		return
	}
	if len(set) == 0 {
		return // nothing to remove from a template without metadata
	}
	block := []string{"metadata:", "  annotations:"}
	for _, k := range sortedKeys(set) {
		block = append(block, "    "+quoteKey(k)+": "+set[k])
	}
	ed.insertBlock(tmpl, block)
}
