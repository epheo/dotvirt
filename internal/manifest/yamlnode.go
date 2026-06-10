package manifest

import "gopkg.in/yaml.v3"

// Helpers for navigating and minimally mutating a yaml.Node mapping tree.
// yaml.v3 stores a mapping's children as a flat slice [key0,val0,key1,val1,...].

// nodeValue safely reads a (possibly nil) scalar node's value, so chained
// lookups like nodeValue(get(meta, "name")) work even when the key is absent.
func nodeValue(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	return n.Value
}

// contentRoot returns the mapping node at the root of a document node.
func contentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		return doc.Content[0]
	}
	if doc.Kind == yaml.MappingNode {
		return doc
	}
	return nil
}

// get returns the value node for key in a mapping, or nil if absent.
func get(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
