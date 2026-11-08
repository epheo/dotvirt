package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/model"
)

// The byte-stable line editor: a generic text splicer over the manifest's
// original lines, steered by yaml.Node positions. It knows nothing about VMs -
// the apply* functions in the edit files own that - and exists so an edit
// rewrites only the touched lines, keeping diffs reviewable and every
// untouched byte (comments, ordering, quoting) intact.

// newLineEditor splits content on "\n" so that a yaml.Node's Line indexes
// lines. yaml.v3 also breaks lines on a bare CR, NEL, LS and PS; a document
// using any of those would put every node position off by one or more lines
// (corrupting the edit, or indexing past the slice), so it is refused. CRLF
// is fine: the "\r" stays on the line and the count agrees.
func newLineEditor(content []byte) (*lineEditor, error) {
	s := string(content)
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\r' && (i+1 == len(s) || s[i+1] != '\n'),
			strings.HasPrefix(s[i:], "\u0085"),
			strings.HasPrefix(s[i:], "\u2028"),
			strings.HasPrefix(s[i:], "\u2029"):
			return nil, errors.New("manifest uses a line break other than LF or CRLF")
		}
	}
	e := &lineEditor{lines: strings.Split(s, "\n")}
	e.markScalarBodies(content)
	return e, nil
}

// markScalarBodies records the lines of every literal or folded scalar's body:
// they are the value, even when one starts with '#' (a "#cloud-config"
// header), so the extent scans must not step over them as comments.
func (e *lineEditor) markScalarBodies(content []byte) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var doc yaml.Node
		if dec.Decode(&doc) != nil {
			return
		}
		e.markBodies(&doc)
	}
}

func (e *lineEditor) markBodies(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		head := n.Line - 1 // the indicator's line; the body is indented past it
		if head < 0 || head >= len(e.lines) {
			return
		}
		indent := indentOf(e.lines[head])
		for i := head + 1; i < len(e.lines); i++ {
			if strings.TrimSpace(e.lines[i]) == "" {
				continue
			}
			if indentOf(e.lines[i]) <= indent {
				break
			}
			if e.body == nil {
				e.body = map[int]bool{}
			}
			e.body[i] = true
		}
		return
	}
	for _, c := range n.Content {
		e.markBodies(c)
	}
}

// lineEditor applies in-place line edits, insertions, and deletions to a file's
// lines, using yaml.Node positions (1-based Line, Column) to target exact spots.
// The first splice it cannot make safely is kept in err, so the appliers stay
// free of error plumbing and ApplyEdit refuses the whole edit.
type lineEditor struct {
	lines   []string
	inserts []insertion  // queued block insertions
	deleted map[int]bool // 0-based line indices to drop
	body    map[int]bool // 0-based lines of block scalar bodies (see markScalarBodies)
	err     error
}

func (e *lineEditor) fail(err error) {
	if e.err == nil {
		e.err = err
	}
}

// inFlow fails the edit when n is a flow collection ({...} or [...]): its
// entries share lines, so a line splice inside it would cut through siblings
// or land beside it instead.
func (e *lineEditor) inFlow(n *yaml.Node) bool {
	if n.Style&yaml.FlowStyle == 0 {
		return false
	}
	e.fail(fmt.Errorf("%w: line %d is written in flow style ({...} or [...]), which the editor cannot change in place; rewrite it as a block in git first", model.ErrConflict, n.Line))
	return true
}

// str is yamlString for a value or key an applier splices; a refusal fails the
// edit.
func (e *lineEditor) str(s string) string {
	out, err := yamlString(s)
	if err != nil {
		e.fail(err)
	}
	return out
}

// yamlString renders s as a one-line YAML string scalar, usable as a mapping
// key or value: plain when both readers that matter take the plain text back
// as the same string, double-quoted otherwise. The readers are yaml.v3
// (dotvirt's own parse) and YAML 1.1 through sigs.k8s.io/yaml (what Argo and
// kubectl apply with), where yes, on and 0x10 are not strings either. Line
// breaks are refused: the editor addresses the file by lines, and a raw break
// in a value opens a new key or document.
func yamlString(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n\u0085\u2028\u2029") {
		return "", fmt.Errorf("%w: %q spans lines", model.ErrInvalid, s)
	}
	for _, text := range []string{s, strconv.Quote(s)} {
		if readsBack(text, s) {
			return text, nil
		}
	}
	return "", fmt.Errorf("%w: %q cannot be written as a YAML string", model.ErrInvalid, s)
}

// readsBack reports whether text, as both key and value of a one-line
// mapping, decodes to the string want under both readers.
func readsBack(text, want string) bool {
	line := []byte(text + ": " + text)
	var doc yaml.Node
	if yaml.Unmarshal(line, &doc) != nil {
		return false
	}
	m := contentRoot(&doc)
	if m == nil || m.Kind != yaml.MappingNode || len(m.Content) != 2 {
		return false
	}
	for _, n := range m.Content {
		if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" || n.Value != want {
			return false
		}
	}
	var v map[string]any
	if sigsyaml.Unmarshal(line, &v) != nil || len(v) != 1 {
		return false
	}
	got, ok := v[want].(string)
	return ok && got == want
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
		if e.inFlow(mapping) {
			return
		}
		start := mapping.Content[i].Line - 1
		if start < 0 || start >= len(e.lines) {
			return
		}
		keyIndent := indentOf(e.lines[start])
		last := start
		for j := start + 1; j < len(e.lines); j++ {
			if e.skippable(j) {
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

// setChild replaces the scalar value of mapping's key in place, keeping the
// key, the indentation and any trailing comment. No-op if the key is absent.
// It refuses what a one-line splice cannot rewrite: a value inside a flow
// collection, one spanning lines, or one an anchor or alias shares.
func (e *lineEditor) setChild(mapping *yaml.Node, key, newVal string) {
	node := get(mapping, key)
	if node == nil || e.inFlow(mapping) {
		return
	}
	idx := node.Line - 1
	if node.Kind != yaml.ScalarNode || node.Anchor != "" || node.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 ||
		idx < 0 || idx >= len(e.lines) || node.Column < 1 || node.Column-1 > len(e.lines[idx]) {
		e.fail(fmt.Errorf("%w: the value of %s at line %d spans lines or is shared through an anchor, which the editor cannot rewrite; edit it in git directly", model.ErrConflict, key, node.Line))
		return
	}
	line, cr := strings.CutSuffix(e.lines[idx], "\r")
	prefix, rest := line[:node.Column-1], line[node.Column-1:]
	end, ok := scalarEnd(rest, node.Style)
	if !ok {
		e.fail(fmt.Errorf("%w: the value of %s at line %d spans lines, which the editor cannot rewrite; edit it in git directly", model.ErrConflict, key, node.Line))
		return
	}
	e.lines[idx] = prefix + newVal + rest[end:]
	if cr {
		e.lines[idx] += "\r"
	}
}

// scalarEnd is where the one-line scalar at the start of rest ends, so only
// the old value is replaced and what follows it (a comment) stays. A tag
// before the value goes with it. ok is false for a quoted value whose closing
// quote is not on this line.
func scalarEnd(rest string, style yaml.Style) (end int, ok bool) {
	i := 0
	if strings.HasPrefix(rest, "!") {
		if i = strings.IndexAny(rest, " \t"); i < 0 {
			return len(rest), true
		}
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
			i++
		}
	}
	switch {
	case style&yaml.DoubleQuotedStyle != 0:
		for j := i + 1; j < len(rest); j++ {
			switch rest[j] {
			case '\\':
				j++
			case '"':
				return j + 1, true
			}
		}
		return 0, false
	case style&yaml.SingleQuotedStyle != 0:
		for j := i + 1; j < len(rest); j++ {
			if rest[j] != '\'' {
				continue
			}
			if j+1 < len(rest) && rest[j+1] == '\'' {
				j++
				continue
			}
			return j + 1, true
		}
		return 0, false
	}
	for j := i; j < len(rest); j++ {
		if rest[j] == '#' && j > 0 && (rest[j-1] == ' ' || rest[j-1] == '\t') {
			return strings.LastIndexFunc(rest[:j], func(r rune) bool { return r != ' ' && r != '\t' }) + 1, true
		}
	}
	return len(strings.TrimRight(rest, " \t")), true
}

// upsertChild is setChild when mapping holds key, insertChild otherwise.
func (e *lineEditor) upsertChild(mapping *yaml.Node, key, val string) {
	if get(mapping, key) != nil {
		e.setChild(mapping, key, val)
		return
	}
	e.insertChild(mapping, key, val)
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
	if mapping == nil || mapping.Kind != yaml.MappingNode || e.inFlow(mapping) || len(mapping.Content) == 0 {
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
		if e.skippable(i) {
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

// skippable reports whether line i carries no YAML content: blank, or a
// comment outside any block scalar body. A comment's indentation says nothing
// about the block around it (a column-0 comment inside a list item is
// common), so extent scans step over these.
func (e *lineEditor) skippable(i int) bool {
	t := strings.TrimSpace(e.lines[i])
	return t == "" || strings.HasPrefix(t, "#") && !e.body[i]
}

// appendItem queues a sequence item (block lines, already including the leading
// "- ") to be inserted after the last existing item of seq, indented to match
// the sequence. Does nothing if seq is nil/empty: there is no item to align
// with, and verifyEdit refuses the edit the missing item leaves unapplied.
func appendItem(ed *lineEditor, seq *yaml.Node, itemLines []string) {
	if seq == nil || seq.Kind != yaml.SequenceNode || ed.inFlow(seq) || len(seq.Content) == 0 {
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
		if ed.skippable(i) {
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
