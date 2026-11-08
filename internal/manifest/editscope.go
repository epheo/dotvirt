package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/epheo/dotvirt/internal/model"
)

// verifyScope compares the whole file before and after an edit, parsed: every
// other document must be unchanged, and in the VM's document only paths the
// edit owns may differ. The line editor splices by line and indentation, so
// a layout it misreads damages siblings of what it edits; the read-back only
// checks what was asked for, this catches everything else.
// strip is the sizing plan's: the sizing fields an instancetype-sized result
// loses.
func verifyScope(before, after []byte, defaultNS, namespace, name string, edit VMEdit, strip []string) error {
	refuse := func(what string) error {
		return fmt.Errorf("%w: the edit of %s/%s would also change %s; the editor cannot change this layout safely; edit it in git directly",
			model.ErrConflict, namespace, name, what)
	}
	bDocs, err := decodeDocs(before)
	if err != nil {
		return fmt.Errorf("%w: the manifest of %s/%s cannot be compared (%v); edit it in git directly", model.ErrConflict, namespace, name, err)
	}
	aDocs, err := decodeDocs(after)
	if err != nil || len(aDocs) != len(bDocs) {
		return refuse("the file's other documents")
	}
	target := -1
	for i, d := range bDocs {
		if target < 0 && isVM(d, defaultNS, namespace, name) {
			target = i
			continue
		}
		if !reflect.DeepEqual(d, aDocs[i]) {
			return refuse(fmt.Sprintf("document %d of the file", i+1))
		}
	}
	if target < 0 {
		return refuse("which document declares the VM")
	}
	b, a := newShape(bDocs[target]), newShape(aDocs[target])
	owned := ownedPaths(edit, bDocs[target], aDocs[target], strip)
	var stray []string
	for p := range pathUnion(b.leaves, a.leaves) {
		if b.leaves[p] != a.leaves[p] && !ownedBy(p, owned) {
			stray = append(stray, readable(p))
		}
	}
	for list, items := range b.order {
		if !slices.Equal(kept(items, a.order[list], owned), kept(a.order[list], items, owned)) {
			stray = append(stray, "the order of "+readable(list))
		}
	}
	if len(stray) > 0 {
		slices.Sort(stray)
		return refuse(strings.Join(stray, ", "))
	}
	return nil
}

func decodeDocs(content []byte) ([]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	var docs []any
	for {
		var d any
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
}

// isVM reports whether doc declares VM (namespace, name), by findVM's rule.
func isVM(doc any, defaultNS, namespace, name string) bool {
	m, _ := doc.(map[string]any)
	meta, _ := m["metadata"].(map[string]any)
	ns, _ := meta["namespace"].(string)
	if ns == "" {
		ns = defaultNS
	}
	return m["kind"] == "VirtualMachine" && meta["name"] == name && ns == namespace
}

// pathSep joins path segments: label keys carry the dots and slashes a readable
// separator would need.
const pathSep = "\x1f"

func docPath(segs ...string) string { return strings.Join(segs, pathSep) }

func readable(p string) string { return strings.ReplaceAll(p, pathSep, ".") }

// shape is a document flattened for comparison: each leaf under its path,
// and the item order of each list keyed by name.
type shape struct {
	leaves map[string]string
	order  map[string][]string // list path -> its item paths, in order
}

func newShape(doc any) shape {
	s := shape{leaves: map[string]string{}, order: map[string][]string{}}
	s.flatten("", doc)
	return s
}

// flatten records each leaf of v under its path, typed so a string "true"
// and a bool differ. Empty maps, empty lists and nulls are leaves too:
// KubeVirt reads meaning from `masquerade: {}` or `efi: {}`. List items are
// keyed by their name (a dataVolumeTemplate's metadata.name) when every item
// has a distinct one, so an append or a removal moves no other item's path;
// their order, which KubeVirt also reads (boot order), is kept apart.
func (s shape) flatten(prefix string, v any) {
	join := func(seg string) string {
		if prefix == "" {
			return seg
		}
		return prefix + pathSep + seg
	}
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			s.leaves[prefix] = "{}"
		}
		for k, c := range t {
			s.flatten(join(k), c)
		}
	case map[any]any:
		if len(t) == 0 {
			s.leaves[prefix] = "{}"
		}
		for k, c := range t {
			s.flatten(join(fmt.Sprint(k)), c)
		}
	case []any:
		if len(t) == 0 {
			s.leaves[prefix] = "[]"
		}
		keys := itemKeys(t)
		for i, c := range t {
			s.flatten(join(keys[i]), c)
			if strings.HasPrefix(keys[i], "[") {
				s.order[prefix] = append(s.order[prefix], join(keys[i]))
			}
		}
	case nil:
		s.leaves[prefix] = "null"
	default:
		s.leaves[prefix] = fmt.Sprintf("%T:%v", t, t)
	}
}

// kept is items without those the edit owns or other lacks: the items whose
// relative order an edit must leave alone.
func kept(items, other []string, owned []string) []string {
	var out []string
	for _, it := range items {
		if !ownedBy(it, owned) && slices.Contains(other, it) {
			out = append(out, it)
		}
	}
	return out
}

func itemKeys(items []any) []string {
	keys := make([]string, len(items))
	seen := map[string]bool{}
	for i, it := range items {
		k := itemName(it)
		if k == "" || seen[k] {
			for j := range items {
				keys[j] = fmt.Sprintf("#%d", j)
			}
			return keys
		}
		seen[k] = true
		keys[i] = "[" + k + "]"
	}
	return keys
}

func itemName(item any) string {
	m, _ := item.(map[string]any)
	if n, ok := m["name"].(string); ok {
		return n
	}
	meta, _ := m["metadata"].(map[string]any)
	n, _ := meta["name"].(string)
	return n
}

func pathUnion(a, b map[string]string) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// ownedBy reports whether path p is owned: an owned path, inside one, or a
// leaf above one (the null or empty container an edit fills or empties).
func ownedBy(p string, owned []string) bool {
	for _, o := range owned {
		if p == o || strings.HasPrefix(p, o+pathSep) || strings.HasPrefix(o, p+pathSep) {
			return true
		}
	}
	return false
}

// ownedPaths is every path edit may change, mirroring what each applier
// writes. before and after are the VM document, for the names an edit
// reaches only through the file (a removed disk's DataVolume, a migrated
// volume's new one); strip is what applySizing deletes beside an instancetype.
func ownedPaths(e VMEdit, before, after any, strip []string) []string {
	spec, tmpl := "spec", docPath("spec", "template")
	tspec := docPath(tmpl, "spec")
	domain := docPath(tspec, "domain")
	devices := docPath(domain, "devices")
	item := func(list, name string) string { return list + pathSep + "[" + name + "]" }

	var own []string
	if e.Power != nil {
		own = append(own, docPath(spec, "running"), docPath(spec, "runStrategy"))
	}
	// applySizing strips what collides with the instancetype of any VM keeping
	// one, whatever the edit, and only that: a shared field the VM keeps beside
	// it must not move. Without one, only the axes asked for move.
	if e.Sizing != nil || e.CPUCores != nil || e.Memory != nil || e.Instancetype != nil || lookup(before, "spec", "instancetype") != nil {
		own = append(own, docPath(spec, "instancetype"))
		if lookup(after, "spec", "instancetype") != nil {
			for _, p := range strip {
				own = append(own, docPath(append([]string{domain}, strings.Split(p, ".")...)...))
			}
		} else {
			own = append(own, docPath(domain, "cpu", "cores"), docPath(domain, "cpu", "sockets"), docPath(domain, "cpu", "threads"),
				docPath(domain, "memory", "guest"), docPath(domain, "resources", "requests", "memory"))
		}
	}
	if e.Preference != nil {
		own = append(own, docPath(spec, "preference"))
	}
	for k := range e.SetLabels {
		own = append(own, docPath("metadata", "labels", k))
	}
	for _, k := range e.RemoveLabels {
		own = append(own, docPath("metadata", "labels", k))
	}
	if e.DRSExclude != nil {
		own = append(own, docPath(tmpl, "metadata", "annotations", PreferNoEvictionAnnotation))
	}
	if e.EvictionStrategy != nil {
		own = append(own, docPath(tspec, "evictionStrategy"))
	}
	vm, _ := lookup(before, "metadata", "name").(string)
	for _, d := range e.AddDisks {
		own = append(own, item(docPath(devices, "disks"), d.Name), item(docPath(tspec, "volumes"), d.Name),
			item(docPath(spec, "dataVolumeTemplates"), vm+"-"+d.Name))
	}
	for _, n := range e.RemoveDisks {
		own = append(own, item(docPath(devices, "disks"), n), item(docPath(tspec, "volumes"), n))
		if dv, ok := backingDV(before, n); ok {
			own = append(own, item(docPath(spec, "dataVolumeTemplates"), dv))
		}
	}
	for _, n := range e.AddNetworks {
		own = append(own, item(docPath(devices, "interfaces"), n.InterfaceName()), item(docPath(tspec, "networks"), n.InterfaceName()))
	}
	for _, n := range e.RemoveNetworks {
		own = append(own, item(docPath(devices, "interfaces"), n), item(docPath(tspec, "networks"), n))
	}
	if len(e.MigrateVolumes) > 0 {
		own = append(own, docPath(spec, "updateVolumesStrategy"))
	}
	for _, m := range e.MigrateVolumes {
		own = append(own, item(docPath(tspec, "volumes"), m.Name))
		for _, doc := range []any{before, after} {
			if dv, ok := backingDV(doc, m.Name); ok {
				own = append(own, item(docPath(spec, "dataVolumeTemplates"), dv))
			}
		}
	}
	if e.Pin != nil || len(e.AddGroups) > 0 || len(e.RemoveGroups) > 0 {
		own = append(own, docPath(tspec, "affinity"), docPath(tspec, "nodeSelector", hostnameLabel))
	}
	for _, g := range e.AddGroups {
		own = append(own, docPath(tmpl, "metadata", "labels", GroupLabelPrefix+g.Name))
	}
	for _, g := range e.RemoveGroups {
		own = append(own, docPath(tmpl, "metadata", "labels", GroupLabelPrefix+g))
	}
	return own
}

// lookup walks doc's mappings along keys, nil when any is absent.
func lookup(doc any, keys ...string) any {
	for _, k := range keys {
		m, ok := doc.(map[string]any)
		if !ok {
			return nil
		}
		doc = m[k]
	}
	return doc
}

// backingDV is the DataVolume backing volume name in doc.
func backingDV(doc any, name string) (string, bool) {
	vols, _ := lookup(doc, "spec", "template", "spec", "volumes").([]any)
	for _, v := range vols {
		if itemName(v) == name {
			dv, ok := lookup(v, "dataVolume", "name").(string)
			return dv, ok
		}
	}
	return "", false
}
