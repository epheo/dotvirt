package manifest

import "testing"

// A null document is no content: a trailing --- or a comment-only document
// must not mark a file as holding something its headers cannot name.
func TestHeadersSkipNullDocuments(t *testing.T) {
	const ns = "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: alpha\n"
	for _, tc := range []struct {
		name    string
		content string
		heads   int
		docs    int
	}{
		{"plain", ns, 1, 1},
		{"trailing separator", ns + "---\n", 1, 1},
		{"comment-only document", ns + "---\n# notes\n", 1, 1},
		{"explicit null", ns + "---\n~\n", 1, 1},
		{"leading separator", "---\n" + ns, 1, 1},
		{"headerless document", ns + "---\nfoo: bar\n", 1, 2},
		{"scalar document", ns + "---\nhello\n", 1, -1},
		{"syntax error", ns + "---\nfoo: [\n", 1, -1},
	} {
		heads, docs := Headers([]byte(tc.content))
		if len(heads) != tc.heads || docs != tc.docs {
			t.Errorf("%s: heads=%d docs=%d, want %d/%d", tc.name, len(heads), docs, tc.heads, tc.docs)
		}
	}
}
