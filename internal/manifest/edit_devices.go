package manifest

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/epheo/dotvirt/internal/model"
)

// applyDisksNetworks adds/removes disks and networks on the VM template. Disks
// touch domain.devices.disks + template.spec.volumes + spec.dataVolumeTemplates;
// networks touch domain.devices.interfaces + template.spec.networks.
func applyDisksNetworks(ed *lineEditor, vmRoot *yaml.Node, edit VMEdit) {
	if len(edit.AddDisks) == 0 && len(edit.RemoveDisks) == 0 &&
		len(edit.AddNetworks) == 0 && len(edit.RemoveNetworks) == 0 {
		return
	}
	tmplSpec := templateSpecNode(vmRoot)
	if tmplSpec == nil {
		return
	}
	spec := get(vmRoot, "spec")
	domain := get(tmplSpec, "domain")
	devices := get(domain, "devices")

	applyAddDisks(ed, spec, tmplSpec, devices, nodeValue(get(get(vmRoot, "metadata"), "name")), edit.AddDisks)
	for _, name := range edit.RemoveDisks {
		removeDisk(ed, spec, tmplSpec, devices, name)
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

// applyAddDisks adds each new disk as a persistent, blank DataVolume: a disk
// device, a dataVolume-backed volume, and a spec.dataVolumeTemplates entry that
// provisions the PVC (on d.StorageClass, or the cluster default when empty). The
// templates section is created when the VM lacks one (e.g. a container-disk
// import); when it already exists — every wizard-built VM — entries are appended.
func applyAddDisks(ed *lineEditor, spec, tmplSpec, devices *yaml.Node, vm string, adds []model.DiskAdd) {
	if len(adds) == 0 {
		return
	}
	templates := get(spec, "dataVolumeTemplates")
	var created []string // a new dataVolumeTemplates section, built when none exists
	for _, d := range adds {
		dv := vm + "-" + d.Name
		size := d.Size
		if size == "" {
			size = "10Gi"
		}
		appendItem(ed, get(devices, "disks"), []string{
			"- name: " + d.Name,
			"  disk:",
			"    bus: virtio",
		})
		appendItem(ed, get(tmplSpec, "volumes"), []string{
			"- name: " + d.Name,
			"  dataVolume:",
			"    name: " + dv,
		})
		item := blankDVTemplate(dv, size, d.StorageClass)
		if templates != nil {
			appendItem(ed, templates, item)
		} else {
			created = append(created, item...)
		}
	}
	if len(created) > 0 {
		ed.insertBlock(spec, append([]string{"dataVolumeTemplates:"}, created...))
	}
}

// blankDVTemplate is a dataVolumeTemplates sequence item (block lines starting
// with "- ") for a blank PVC named dv. storageClassName is emitted only when a
// class is set, mirroring vmgen's storageBlock.
func blankDVTemplate(dv, size, class string) []string {
	lines := []string{
		"- metadata:",
		"    name: " + dv,
		"  spec:",
		"    source:",
		"      blank: {}",
		"    storage:",
		"      resources:",
		"        requests:",
		"          storage: " + size,
	}
	if class != "" {
		lines = append(lines, "      storageClassName: "+class)
	}
	return lines
}

// removeDisk deletes a disk device and its volume, plus — when the volume was
// DataVolume-backed — the dataVolumeTemplates entry that provisioned it, so no
// orphaned PVC template is left behind.
func removeDisk(ed *lineEditor, spec, tmplSpec, devices *yaml.Node, name string) {
	volumes := get(tmplSpec, "volumes")
	if vol := namedItem(volumes, name); vol != nil {
		if dv := get(get(vol, "dataVolume"), "name"); dv != nil {
			removeTemplateNamed(ed, get(spec, "dataVolumeTemplates"), dv.Value)
		}
	}
	removeNamedItem(ed, get(devices, "disks"), name)
	removeNamedItem(ed, volumes, name)
}

// removeTemplateNamed deletes the dataVolumeTemplates item whose metadata.name
// equals name — their identity, unlike the top-level name: of disks and volumes.
func removeTemplateNamed(ed *lineEditor, seq *yaml.Node, name string) {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	for i, item := range seq.Content {
		if nodeValue(get(get(item, "metadata"), "name")) != name {
			continue
		}
		ed.removeRange(item.Line-1, itemEndLine(ed, seq, i))
		return
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
