package manifest

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/epheo/dotvirt/internal/model"
)

// VMEdit lives in model so the API request, the persisted draft store, and
// this editor share one definition; the alias keeps manifest's signatures.
type VMEdit = model.VMEdit

// ApplyEdit edits the VirtualMachine named (namespace, name) within a manifest,
// changing only the targeted fields. It works by splicing new values into the
// original text at the exact lines of the target scalars - never re-serializing
// the document - so the resulting diff touches only the changed lines and all
// formatting, comments, and key order are preserved byte-for-byte elsewhere.
//
// yaml.v3's encoder reformats sequences on round-trip, so a node-tree re-marshal
// would produce noisy diffs; line splicing avoids that entirely.
func ApplyEdit(content []byte, namespace, name string, edit VMEdit) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	vm := findVM(&root, namespace, name)
	if vm == nil {
		return nil, fmt.Errorf("VM %s/%s not found in manifest", namespace, name)
	}

	ed := &lineEditor{lines: splitLines(content)}

	if edit.Power != nil {
		applyPower(ed, vm, *edit.Power)
	}
	applySizing(ed, vm, edit)
	if edit.Preference != nil {
		applyRef(ed, vm, "preference", *edit.Preference)
	}
	applyMetadata(ed, vm, edit)
	// Scheduling queues its affinity rewrite before applyTemplateMeta: when a
	// template lacks metadata, both insert blocks anchor on the same last
	// line, and the affinity lines (inside spec) must land before the new
	// metadata: sibling.
	schedSet, schedRemove, err := applySchedulingRules(ed, vm, edit)
	if err != nil {
		return nil, err
	}
	applyTemplateMeta(ed, vm, schedSet, schedRemove, edit)
	if edit.EvictionStrategy != nil {
		applyEvictionStrategy(ed, vm, *edit.EvictionStrategy)
	}
	applyDisksNetworks(ed, vm, edit)
	applyVolumeMigrations(ed, vm, edit.MigrateVolumes)

	return ed.bytes(), nil
}

// applyEvictionStrategy sets (or, for "", removes) the template's
// evictionStrategy - whether an eviction live-migrates the VM (LiveMigrate),
// or is refused outright (None: pinned, blocks node drains too).
func applyEvictionStrategy(ed *lineEditor, vmRoot *yaml.Node, strategy string) {
	s := get(get(get(vmRoot, "spec"), "template"), "spec")
	if s == nil {
		return
	}
	if es := get(s, "evictionStrategy"); es != nil {
		if strategy == "" {
			ed.deleteChild(s, "evictionStrategy")
			return
		}
		ed.setScalarAt(es, strategy)
		return
	}
	if strategy != "" {
		ed.insertChild(s, "evictionStrategy", strategy)
	}
}

// applySizing writes the VM's CPU/memory in exactly one representation - an
// instancetype reference or inline domain.cpu/domain.memory - never both, which
// KubeVirt's webhook rejects. The Sizing mode picks which:
//
//   - "custom": drop spec.instancetype, then apply inline cpu/memory.
//   - "instancetype": apply the instancetype ref, then strip any inline cpu/memory.
//   - "" (mode unset, e.g. power/label/device-only edits or older clients): apply
//     fields field-by-field, but if the VM carries an instancetype, never write
//     inline cpu/memory - strip any that slipped in. This normalizes a VM that was
//     wrongly given both (the conflict that fails the webhook) on any later edit.
//
// Preference is independent (it never defines cpu/memory) and is applied by the
// caller in every mode.
func applySizing(ed *lineEditor, vm *yaml.Node, edit VMEdit) {
	mode := ""
	if edit.Sizing != nil {
		mode = *edit.Sizing
	}
	switch mode {
	case "custom":
		stripRef(ed, vm, "instancetype")
		applyInline(ed, vm, edit)
	case "instancetype":
		if edit.Instancetype != nil {
			applyRef(ed, vm, "instancetype", *edit.Instancetype)
		}
		stripInlineSizing(ed, vm)
	default:
		hasIT := get(get(vm, "spec"), "instancetype") != nil
		setsIT := edit.Instancetype != nil && *edit.Instancetype != ""
		if edit.Instancetype != nil {
			applyRef(ed, vm, "instancetype", *edit.Instancetype)
		}
		if hasIT || setsIT {
			stripInlineSizing(ed, vm) // never both - instancetype wins
		} else {
			applyInline(ed, vm, edit)
		}
	}
}

func applyInline(ed *lineEditor, vm *yaml.Node, edit VMEdit) {
	if edit.CPUCores != nil {
		applyCPU(ed, vm, *edit.CPUCores)
	}
	if edit.Memory != nil {
		applyMemory(ed, vm, *edit.Memory)
	}
}

// stripRef removes spec.<key> (e.g. instancetype) entirely, leaving siblings such
// as preference intact.
func stripRef(ed *lineEditor, vmRoot *yaml.Node, key string) {
	ed.deleteChild(get(vmRoot, "spec"), key)
}

// stripInlineSizing removes the inline cpu/memory the instancetype owns:
// domain.cpu, domain.memory, and the legacy resources.requests cpu/memory entries
// (leaving any other resources.requests keys in place).
func stripInlineSizing(ed *lineEditor, vmRoot *yaml.Node) {
	domain := domainNode(vmRoot)
	if domain == nil {
		return
	}
	ed.deleteChild(domain, "cpu")
	ed.deleteChild(domain, "memory")
	if reqs := get(get(domain, "resources"), "requests"); reqs != nil {
		ed.deleteChild(reqs, "cpu")
		ed.deleteChild(reqs, "memory")
	}
}

// applyRef sets spec.<key>.name (used for instancetype/preference), creating the
// block if absent.
func applyRef(ed *lineEditor, vmRoot *yaml.Node, key, value string) {
	spec := get(vmRoot, "spec")
	if spec == nil {
		return
	}
	if ref := get(spec, key); ref != nil {
		if nameNode := get(ref, "name"); nameNode != nil {
			ed.setScalarAt(nameNode, value)
			return
		}
		ed.insertChild(ref, "name", value)
		return
	}
	ed.insertBlock(spec, []string{key + ":", "  name: " + value})
}

// findVM locates the VirtualMachine mapping node for (namespace, name) across all
// documents in the file.
func findVM(root *yaml.Node, namespace, name string) *yaml.Node {
	var docs []*yaml.Node
	if root.Kind == yaml.DocumentNode {
		docs = []*yaml.Node{root}
	}
	// Multi-document files parse as a sequence of documents only when decoded in a
	// loop; a single Unmarshal yields one DocumentNode. Handle both: walk content.
	candidates := docs
	if len(candidates) == 0 && len(root.Content) > 0 {
		candidates = root.Content
	}
	for _, doc := range candidates {
		m := contentRoot(doc)
		if m == nil {
			continue
		}
		if nodeValue(get(m, "kind")) != "VirtualMachine" {
			continue
		}
		meta := get(m, "metadata")
		if meta == nil || nodeValue(get(meta, "name")) != name {
			continue
		}
		if ns := nodeValue(get(meta, "namespace")); ns != "" && ns != namespace {
			continue
		}
		return m
	}
	return nil
}

func applyPower(ed *lineEditor, spec *yaml.Node, power string) {
	s := get(spec, "spec")
	if s == nil {
		return
	}
	on := power == "On"
	if running := get(s, "running"); running != nil {
		ed.setScalarAt(running, boolStr(on))
		return
	}
	if rs := get(s, "runStrategy"); rs != nil {
		ed.setScalarAt(rs, runStrategyFor(on))
		return
	}
	// Neither present: insert runStrategy as the first child of spec.
	ed.insertChild(s, "runStrategy", runStrategyFor(on))
}

func applyCPU(ed *lineEditor, vmRoot *yaml.Node, cores int) {
	domain := domainNode(vmRoot)
	if domain == nil {
		return
	}
	val := fmt.Sprintf("%d", cores)
	if cpu := get(domain, "cpu"); cpu != nil {
		if c := get(cpu, "cores"); c != nil {
			ed.setScalarAt(c, val)
			return
		}
		ed.insertChild(cpu, "cores", val)
		return
	}
	// No cpu block: insert "cpu:\n  cores: N" under domain.
	ed.insertBlock(domain, []string{"cpu:", "  cores: " + val})
}

func applyMemory(ed *lineEditor, vmRoot *yaml.Node, memory string) {
	domain := domainNode(vmRoot)
	if domain == nil {
		return
	}
	if mem := get(domain, "memory"); mem != nil {
		if g := get(mem, "guest"); g != nil {
			ed.setScalarAt(g, memory)
			return
		}
		ed.insertChild(mem, "guest", memory)
		return
	}
	if res := get(domain, "resources"); res != nil {
		if reqs := get(res, "requests"); reqs != nil {
			if m := get(reqs, "memory"); m != nil {
				ed.setScalarAt(m, memory)
				return
			}
		}
	}
	ed.insertBlock(domain, []string{"memory:", "  guest: " + memory})
}

func domainNode(vmRoot *yaml.Node) *yaml.Node {
	s := get(vmRoot, "spec")
	if s == nil {
		return nil
	}
	tmpl := get(s, "template")
	if tmpl == nil {
		return nil
	}
	ts := get(tmpl, "spec")
	if ts == nil {
		return nil
	}
	return get(ts, "domain")
}

func runStrategyFor(on bool) string {
	if on {
		return "Always"
	}
	return "Halted"
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
