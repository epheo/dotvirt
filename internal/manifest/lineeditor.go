package manifest

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// The byte-stable line editor: a generic text splicer over the manifest's
// original lines, steered by yaml.Node positions. It knows nothing about VMs -
// the apply* functions in the edit files own that - and exists so an edit
// rewrites only the touched lines, keeping diffs reviewable and every
// untouched byte (comments, ordering, quoting) intact.

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

// deleteChild removes the "key: ..." entry of a block mapping, including its full
// nested value: from the key's line down through the last line indented deeper
// than the key (the same indent scan used to find a block's extent elsewhere).
// No-op if the mapping or key is absent.
func (e *lineEditor) deleteChild(mapping *yaml.Node, key string) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != key {
			continue
		}
		start := mapping.Content[i].Line - 1
		if start < 0 || start >= len(e.lines) {
			return
		}
		keyIndent := indentOf(e.lines[start])
		last := start
		for j := start + 1; j < len(e.lines); j++ {
			if strings.TrimSpace(e.lines[j]) == "" {
				continue
			}
			if indentOf(e.lines[j]) <= keyIndent {
				break
			}
			last = j
		}
		e.removeRange(start, last+1)
		return
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
