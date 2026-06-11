package export

import (
	"testing"

	"github.com/epheo/dotvirt/internal/git"
)

func TestExportSignature(t *testing.T) {
	a := []git.File{{Path: "ns/b.yaml", Content: []byte("B")}, {Path: "ns/a.yaml", Content: []byte("A")}}
	dirs := []string{"ns"}

	// Order-independent (paths are sorted internally).
	reordered := []git.File{a[1], a[0]}
	if exportSignature(a, dirs) != exportSignature(reordered, dirs) {
		t.Error("signature should be independent of file order")
	}

	// Sensitive to content.
	changed := []git.File{{Path: "ns/a.yaml", Content: []byte("A")}, {Path: "ns/b.yaml", Content: []byte("B2")}}
	if exportSignature(a, dirs) == exportSignature(changed, dirs) {
		t.Error("signature should change when a file's content changes")
	}

	// Sensitive to the managed-dir set (a namespace coming/going must re-export).
	if exportSignature(a, dirs) == exportSignature(a, []string{"ns", "ns2"}) {
		t.Error("signature should change when the managed namespaces change")
	}

	// Sensitive to a removed file (VM deleted).
	fewer := []git.File{{Path: "ns/a.yaml", Content: []byte("A")}}
	if exportSignature(a, dirs) == exportSignature(fewer, dirs) {
		t.Error("signature should change when a file is removed")
	}
}
