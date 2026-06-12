package manifest

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/epheo/dotvirt/internal/model"
)

// VMEdit is a set of field changes to apply to a VirtualMachine manifest. Nil/
// empty fields are left untouched, so the UI can change just one thing. JSON
// tags let it round-trip through the persisted draft store.
type VMEdit struct {
	Power        *string `json:"power,omitempty"` // "On" | "Off" -> runStrategy Always/Halted (or legacy running bool)
	CPUCores     *int    `json:"cpuCores,omitempty"`
	Memory       *string `json:"memory,omitempty"`       // e.g. "4Gi"
	Instancetype *string `json:"instancetype,omitempty"` // spec.instancetype.name
	Preference   *string `json:"preference,omitempty"`   // spec.preference.name

	// Metadata edits: keys to set (upsert) and keys to remove.
	SetLabels         map[string]string `json:"setLabels,omitempty"`
	RemoveLabels      []string          `json:"removeLabels,omitempty"`
	SetAnnotations    map[string]string `json:"setAnnotations,omitempty"`
	RemoveAnnotations []string          `json:"removeAnnotations,omitempty"`

	// Disk/network edits on the VM template. The add-entries are model types so
	// the API request, the persisted draft, and this edit share one definition.
	AddDisks       []model.DiskAdd    `json:"addDisks,omitempty"`
	RemoveDisks    []string           `json:"removeDisks,omitempty"` // disk names to remove
	AddNetworks    []model.NetworkAdd `json:"addNetworks,omitempty"`
	RemoveNetworks []string           `json:"removeNetworks,omitempty"` // network/interface names to remove
}

// Empty reports whether the edit changes nothing.
func (e VMEdit) Empty() bool {
	return e.Power == nil && e.CPUCores == nil && e.Memory == nil &&
		e.Instancetype == nil && e.Preference == nil &&
		len(e.SetLabels) == 0 && len(e.RemoveLabels) == 0 &&
		len(e.SetAnnotations) == 0 && len(e.RemoveAnnotations) == 0 &&
		len(e.AddDisks) == 0 && len(e.RemoveDisks) == 0 &&
		len(e.AddNetworks) == 0 && len(e.RemoveNetworks) == 0
}

// ApplyEdit edits the VirtualMachine named (namespace, name) within a manifest,
// changing only the targeted fields. It works by splicing new values into the
// original text at the exact lines of the target scalars — never re-serializing
// the document — so the resulting diff touches only the changed lines and all
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
	if edit.CPUCores != nil {
		applyCPU(ed, vm, *edit.CPUCores)
	}
	if edit.Memory != nil {
		applyMemory(ed, vm, *edit.Memory)
	}
	if edit.Instancetype != nil {
		applyRef(ed, vm, "instancetype", *edit.Instancetype)
	}
	if edit.Preference != nil {
		applyRef(ed, vm, "preference", *edit.Preference)
	}
	applyMetadata(ed, vm, edit)
	applyDisksNetworks(ed, vm, edit)

	return ed.bytes(), nil
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

func splitLines(content []byte) []string {
	return strings.Split(string(content), "\n")
}

// lineEditor applies in-place line edits, insertions, and deletions to a file's
// lines, using yaml.Node positions (1-based Line, Column) to target exact spots.
type lineEditor struct {
	lines   []string
	inserts []insertion  // queued block insertions
	deleted map[int]bool // 0-based line indices to drop
}

type insertion struct {
	afterLine int // 0-based index to insert after
	text      []string
}

func (e *lineEditor) markDeleted(i int) {
	if e.deleted == nil {
		e.deleted = map[int]bool{}
	}
	e.deleted[i] = true
}

// removeLine marks a single 0-based line for deletion.
func (e *lineEditor) removeLine(i int) {
	if i >= 0 && i < len(e.lines) {
		e.markDeleted(i)
	}
}

// removeRange marks lines [start, end) (0-based) for deletion.
func (e *lineEditor) removeRange(start, end int) {
	for i := start; i < end && i < len(e.lines); i++ {
		if i >= 0 {
			e.markDeleted(i)
		}
	}
}

// setScalarAt replaces the value of a scalar node in place, keeping the key and
// indentation, by rewriting "<indent>key: <newval>" from the node's position.
func (e *lineEditor) setScalarAt(node *yaml.Node, newVal string) {
	idx := node.Line - 1
	if idx < 0 || idx >= len(e.lines) {
		return
	}
	line := e.lines[idx]
	// The scalar starts at node.Column (1-based). Everything before it (indent +
	// "key: ") stays; replace from the value onward, preserving trailing comments.
	prefixLen := node.Column - 1
	if prefixLen < 0 || prefixLen > len(line) {
		return
	}
	prefix := line[:prefixLen]
	rest := line[prefixLen:]
	e.lines[idx] = prefix + newVal + trailingComment(rest)
}

// insertChild queues "key: val" as a new entry of a block mapping, aligned with
// the mapping's existing children and placed after the last one.
func (e *lineEditor) insertChild(mapping *yaml.Node, key, val string) {
	e.insertBlock(mapping, []string{key + ": " + val})
}

// insertBlock queues a multi-line block as new children of a block mapping. For
// a non-empty block mapping, yaml.v3 reports the mapping node's Line/Column at
// its first child, so children align at mapping.Column and we anchor the insert
// after the mapping's last child line.
func (e *lineEditor) insertBlock(mapping *yaml.Node, block []string) {
	if mapping == nil || mapping.Kind != yaml.MappingNode || len(mapping.Content) == 0 {
		return
	}
	indent := mapping.Column - 1
	pad := strings.Repeat(" ", indent)
	out := make([]string, len(block))
	for i, l := range block {
		out[i] = pad + l
	}
	e.inserts = append(e.inserts, insertion{afterLine: e.mappingLastLine(mapping), text: out})
}

// mappingLastLine returns the 0-based index of the last line belonging to a
// block mapping: it scans from the mapping's first child down to the last line
// indented deeper than (or as deep as) the mapping's own column.
func (e *lineEditor) mappingLastLine(mapping *yaml.Node) int {
	col := mapping.Column // 1-based column of the children
	start := mapping.Line - 1
	last := start
	for i := start; i < len(e.lines); i++ {
		if strings.TrimSpace(e.lines[i]) == "" {
			continue
		}
		// Children sit at indent == col-1; anything shallower ends the mapping.
		if i > start && indentOf(e.lines[i]) < col-1 {
			break
		}
		last = i
	}
	return last
}

func (e *lineEditor) bytes() []byte {
	// Build the output line-by-line: emit each original line unless deleted,
	// expanding any block queued to insert after it. Working in original-index
	// order keeps insert anchors and deletion indices valid simultaneously.
	insertAfter := map[int][]string{}
	for _, ins := range e.inserts {
		insertAfter[ins.afterLine] = append(insertAfter[ins.afterLine], ins.text...)
	}

	var out []string
	for i, line := range e.lines {
		if !e.deleted[i] {
			out = append(out, line)
		}
		if blk, ok := insertAfter[i]; ok {
			out = append(out, blk...)
		}
	}
	return []byte(strings.Join(out, "\n"))
}

// trailingComment returns any " # ..." comment at the end of a value segment, so
// rewriting a value preserves an inline comment.
func trailingComment(rest string) string {
	if i := strings.Index(rest, " #"); i >= 0 {
		return rest[i:]
	}
	return ""
}
