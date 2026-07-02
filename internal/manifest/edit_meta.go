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
	m := get(parent, field)

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

// quoteKey quotes a map key if it contains characters (like '/') that need it.
func quoteKey(k string) string {
	for _, r := range k {
		if r == '/' || r == '.' || r == ':' {
			return fmt.Sprintf("%q", k)
		}
	}
	return k
}
