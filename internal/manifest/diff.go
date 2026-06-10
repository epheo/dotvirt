package manifest

import (
	"fmt"
	"strings"
)

// UnifiedDiff renders a minimal unified diff between two versions of a file.
// Since dotvirt edits change only a handful of lines in place, this emits a
// compact line-by-line diff with a little surrounding context — enough for the
// UI's "what will this commit change" preview.
func UnifiedDiff(path string, before, after []byte) string {
	a := strings.Split(strings.TrimRight(string(before), "\n"), "\n")
	b := strings.Split(strings.TrimRight(string(after), "\n"), "\n")

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n", path, path)

	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	const ctx = 2
	emitted := map[int]bool{}
	for i := 0; i < n; i++ {
		if lineAt(a, i) == lineAt(b, i) {
			continue
		}
		// Print a small context window around each changed line.
		for c := i - ctx; c < i; c++ {
			if c >= 0 && !emitted[c] && lineAt(a, c) == lineAt(b, c) {
				fmt.Fprintf(&sb, " %s\n", lineAt(a, c))
				emitted[c] = true
			}
		}
		if i < len(a) {
			fmt.Fprintf(&sb, "-%s\n", a[i])
		}
		if i < len(b) {
			fmt.Fprintf(&sb, "+%s\n", b[i])
		}
		emitted[i] = true
		for c := i + 1; c <= i+ctx; c++ {
			if c < n && !emitted[c] && lineAt(a, c) == lineAt(b, c) {
				fmt.Fprintf(&sb, " %s\n", lineAt(a, c))
				emitted[c] = true
			}
		}
	}
	return sb.String()
}

func lineAt(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return ""
	}
	return lines[i]
}
