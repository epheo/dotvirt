package manifest

import (
	"slices"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

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

// Kinds reads each document's group from its apiVersion, the core group
// included, whether or not the document is named.
func TestKinds(t *testing.T) {
	content := "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: a\n---\n" +
		"apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\n---\n" +
		"kind: NoVersion\n---\n"
	got := Kinds([]byte(content))
	want := []schema.GroupKind{{Kind: "Namespace"}, {Group: "rbac.authorization.k8s.io", Kind: "RoleBinding"}}
	if !slices.Equal(got, want) {
		t.Errorf("Kinds = %v, want %v", got, want)
	}
}

// Docs names what Headers names, each with its apiVersion's group, and counts
// the same documents.
func TestDocs(t *testing.T) {
	content := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: a\n---\n" +
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n---\n" +
		"apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\n---\n"
	got, docs := Docs([]byte(content))
	want := []Doc{
		{ObjectRef: model.ObjectRef{Kind: "Deployment", Namespace: "a", Name: "web"}, Group: "apps"},
		{ObjectRef: model.ObjectRef{Kind: "ConfigMap", Name: "c"}},
	}
	heads, hdocs := Headers([]byte(content))
	if !slices.Equal(got, want) || docs != 3 || docs != hdocs || len(heads) != len(got) {
		t.Errorf("Docs = %v, %d; want %v, 3 (Headers %v, %d)", got, docs, want, heads, hdocs)
	}
}
