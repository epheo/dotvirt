package git

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// applyDisksNetworks adds/removes disks and networks on the VM template. Disks
// touch domain.devices.disks + template.spec.volumes; networks touch
// domain.devices.interfaces + template.spec.networks.
func applyDisksNetworks(ed *lineEditor, vmRoot *yaml.Node, edit VMEdit) {
	if len(edit.AddDisks) == 0 && len(edit.RemoveDisks) == 0 &&
		len(edit.AddNetworks) == 0 && len(edit.RemoveNetworks) == 0 {
		return
	}
	tmplSpec := templateSpecNode(vmRoot)
	if tmplSpec == nil {
		return
	}
	domain := get(tmplSpec, "domain")
	devices := get(domain, "devices")

	for _, d := range edit.AddDisks {
		appendItem(ed, get(devices, "disks"), []string{
			"- name: " + d.Name,
			"  disk:",
			"    bus: virtio",
		})
		appendItem(ed, get(tmplSpec, "volumes"), []string{
			"- name: " + d.Name,
			"  emptyDisk:",
			"    capacity: " + d.Size,
		})
	}
	for _, name := range edit.RemoveDisks {
		removeNamedItem(ed, get(devices, "disks"), name)
		removeNamedItem(ed, get(tmplSpec, "volumes"), name)
	}

	for _, n := range edit.AddNetworks {
		iface := ifaceName(n.Name)
		appendItem(ed, get(devices, "interfaces"), []string{
			"- name: " + iface,
			"  bridge: {}",
		})
		appendItem(ed, get(tmplSpec, "networks"), []string{
			"- name: " + iface,
			"  multus:",
			"    networkName: " + n.Name,
		})
	}
	for _, name := range edit.RemoveNetworks {
		removeNamedItem(ed, get(devices, "interfaces"), name)
		removeNamedItem(ed, get(tmplSpec, "networks"), name)
	}
}

func templateSpecNode(vmRoot *yaml.Node) *yaml.Node {
	spec := get(vmRoot, "spec")
	tmpl := get(spec, "template")
	return get(tmpl, "spec")
}

func ifaceName(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// appendItem queues a sequence item (block lines, already including the leading
// "- ") to be inserted after the last existing item of seq, indented to match
// the sequence. Does nothing if seq is nil/empty (we only edit VMs that already
// have the relevant device lists).
func appendItem(ed *lineEditor, seq *yaml.Node, itemLines []string) {
	if seq == nil || seq.Kind != yaml.SequenceNode || len(seq.Content) == 0 {
		return
	}
	last := seq.Content[len(seq.Content)-1]
	indent := last.Column - 1 - 2 // items sit 2 cols after the "- "; dash is at item col-2
	if indent < 0 {
		indent = last.Column - 1
	}
	pad := strings.Repeat(" ", indent)

	insertLine := lastLineOfItem(ed, seq, len(seq.Content)-1)
	block := make([]string, len(itemLines))
	for i, l := range itemLines {
		block[i] = pad + l
	}
	ed.inserts = append(ed.inserts, insertion{afterLine: insertLine, text: block})
}

// removeNamedItem deletes the sequence item whose `name:` equals name.
func removeNamedItem(ed *lineEditor, seq *yaml.Node, name string) {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	for i, item := range seq.Content {
		if nodeValue(get(item, "name")) != name {
			continue
		}
		start := item.Line - 1
		end := itemEndLine(ed, seq, i)
		ed.removeRange(start, end)
		return
	}
}

// lastLineOfItem returns the 0-based index of the last line belonging to
// sequence item idx (used as the insert anchor for appending after it).
func lastLineOfItem(ed *lineEditor, seq *yaml.Node, idx int) int {
	return itemEndLine(ed, seq, idx) - 1
}

// itemEndLine returns the 0-based line index just past sequence item idx: the
// next item's start, or (for the last item) the first line dedented to at or
// below the item's own indentation.
func itemEndLine(ed *lineEditor, seq *yaml.Node, idx int) int {
	item := seq.Content[idx]
	startLine := item.Line - 1
	itemIndent := indentOf(ed.lines[startLine])

	if idx+1 < len(seq.Content) {
		return seq.Content[idx+1].Line - 1
	}
	// Last item: scan forward until a line with indent <= the item's dash indent.
	// Track the last non-blank content line so trailing blank lines aren't counted
	// as part of the item (which would otherwise misplace an append after them).
	lastContent := startLine
	for i := startLine + 1; i < len(ed.lines); i++ {
		if strings.TrimSpace(ed.lines[i]) == "" {
			continue
		}
		if indentOf(ed.lines[i]) <= itemIndent {
			return i
		}
		lastContent = i
	}
	return lastContent + 1
}

func indentOf(line string) int {
	n := 0
	for _, r := range line {
		if r == ' ' {
			n++
		} else {
			break
		}
	}
	return n
}
