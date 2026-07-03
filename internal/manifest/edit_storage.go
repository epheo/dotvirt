package manifest

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/epheo/dotvirt/internal/model"
)

// applyVolumeMigrations rewrites each migrated disk's backing DataVolume — a
// fresh name on the target storage class, provisioned blank — repoints the
// template volume at it, and sets spec.updateVolumesStrategy: Migration so
// KubeVirt live-copies a running disk instead of requiring a restart.
// Reverting the commit restores the previous volume set, which KubeVirt reads
// as a migration cancel — the GitOps rollback IS the cancellation.
//
// Only DataVolume-backed volumes with a dataVolumeTemplates entry qualify;
// anything else is left untouched (same silent-no-op convention as the other
// appliers — the UI only offers eligible disks).
func applyVolumeMigrations(ed *lineEditor, vmRoot *yaml.Node, migrations []model.VolumeMigration) {
	if len(migrations) == 0 {
		return
	}
	spec := get(vmRoot, "spec")
	tmplSpec := templateSpecNode(vmRoot)
	templates := get(spec, "dataVolumeTemplates")
	volumes := get(tmplSpec, "volumes")
	if templates == nil || volumes == nil {
		return
	}
	applied := false
	for _, m := range migrations {
		if migrateOneVolume(ed, templates, volumes, m) {
			applied = true
		}
	}
	if !applied {
		return
	}
	if uvs := get(spec, "updateVolumesStrategy"); uvs != nil {
		ed.setScalarAt(uvs, "Migration")
	} else {
		ed.insertChild(spec, "updateVolumesStrategy", "Migration")
	}
}

func migrateOneVolume(ed *lineEditor, templates, volumes *yaml.Node, m model.VolumeMigration) bool {
	dvName := get(get(namedItem(volumes, m.Name), "dataVolume"), "name")
	if dvName == nil {
		return false // not DataVolume-backed
	}
	tmpl := templateNamed(templates, dvName.Value)
	tmplSpec := get(tmpl, "spec")
	storage := get(tmplSpec, "storage")
	if storage == nil {
		storage = get(tmplSpec, "pvc") // legacy PVC-spec form
	}
	if storage == nil {
		return false
	}

	newName := nextDVName(dvName.Value, templates)
	ed.setScalarAt(get(get(tmpl, "metadata"), "name"), newName)
	ed.setScalarAt(dvName, newName)

	if sc := get(storage, "storageClassName"); sc != nil {
		ed.setScalarAt(sc, m.StorageClass)
	} else {
		ed.insertChild(storage, "storageClassName", m.StorageClass)
	}

	// The destination provisions blank: its content comes from the live copy,
	// so importing the original source again would only be overwritten.
	if get(get(tmplSpec, "source"), "blank") == nil {
		ed.deleteChild(tmplSpec, "source")
		ed.deleteChild(tmplSpec, "sourceRef")
		ed.insertBlock(tmplSpec, []string{"source:", "  blank: {}"})
	}
	return true
}

var migSuffix = regexp.MustCompile(`-mig-\d+$`)

// nextDVName derives the replacement DataVolume's name: the old name with any
// prior -mig-N suffix stripped, then the first -mig-N not already taken by a
// template — so repeated migrations cycle x → x-mig-1 → x-mig-2 rather than
// accreting suffixes. The name must change: KubeVirt identifies the migration
// destination as a different volume.
func nextDVName(old string, templates *yaml.Node) string {
	base := migSuffix.ReplaceAllString(old, "")
	taken := map[string]bool{old: true}
	for _, item := range templates.Content {
		taken[nodeValue(get(get(item, "metadata"), "name"))] = true
	}
	for n := 1; ; n++ {
		name := fmt.Sprintf("%s-mig-%d", base, n)
		if !taken[name] {
			return name
		}
	}
}

// namedItem returns the sequence item whose `name:` equals name, or nil.
func namedItem(seq *yaml.Node, name string) *yaml.Node {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	for _, item := range seq.Content {
		if nodeValue(get(item, "name")) == name {
			return item
		}
	}
	return nil
}

// templateNamed returns the dataVolumeTemplates item whose metadata.name
// equals name, or nil.
func templateNamed(seq *yaml.Node, name string) *yaml.Node {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	for _, item := range seq.Content {
		if nodeValue(get(get(item, "metadata"), "name")) == name {
			return item
		}
	}
	return nil
}
