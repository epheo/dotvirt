package manifest

import (
	"bytes"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/vmgen"
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
		iface := model.InterfaceName(n.Name)
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
// import); when it already exists - every wizard-built VM - entries are appended.
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

// blankDVTemplate renders the wizard's blank DataVolume as the sequence-item
// lines the line editor splices, so a disk added later and a disk created with
// the VM are one shape.
func blankDVTemplate(dv, size, class string) []string {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	// A map of string scalars always encodes.
	_ = enc.Encode(vmgen.BlankDataVolumeTemplate(dv, size, class))
	_ = enc.Close()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i, l := range lines {
		if i == 0 {
			lines[i] = "- " + l
		} else {
			lines[i] = "  " + l
		}
	}
	return lines
}

// removeDisk deletes a disk device and its volume, plus - when the volume was
// DataVolume-backed - the dataVolumeTemplates entry that provisioned it, so no
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
// equals name - their identity, unlike the top-level name: of disks and volumes.
func removeTemplateNamed(ed *lineEditor, seq *yaml.Node, name string) {
	if i, item := findNamed(seq, name, true); item != nil {
		ed.removeRange(item.Line-1, itemEndLine(ed, seq, i))
	}
}

func templateSpecNode(vmRoot *yaml.Node) *yaml.Node {
	spec := get(vmRoot, "spec")
	tmpl := get(spec, "template")
	return get(tmpl, "spec")
}

// removeNamedItem deletes the sequence item whose `name:` equals name.
func removeNamedItem(ed *lineEditor, seq *yaml.Node, name string) {
	if i, item := findNamed(seq, name, false); item != nil {
		ed.removeRange(item.Line-1, itemEndLine(ed, seq, i))
	}
}
