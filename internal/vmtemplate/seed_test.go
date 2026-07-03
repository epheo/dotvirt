package vmtemplate

import (
	"strings"
	"testing"
)

// Every seed must parse cleanly and render a valid VirtualMachine with its
// defaults alone — a fresh project's library works before any editing.
func TestSeedsParseAndRenderWithDefaults(t *testing.T) {
	for _, f := range SeedFiles() {
		t.Run(f.Path, func(t *testing.T) {
			if !strings.HasPrefix(f.Path, Dir+"/") {
				t.Fatalf("seed outside %s/: %s", Dir, f.Path)
			}
			tpl := Parse(f.Path, f.Content, "seed")
			if tpl.Error != "" {
				t.Fatalf("parse: %s", tpl.Error)
			}
			if tpl.Description == "" {
				t.Error("seed has no description")
			}
			r, err := EngineRenderer{}.Render(f.Content, nil, "tenant-a")
			if err != nil {
				t.Fatalf("render with defaults: %v", err)
			}
			if !strings.HasPrefix(r.Name, "fedora-") {
				t.Fatalf("generated name %q", r.Name)
			}
			if strings.Contains(string(r.Manifest), "${") {
				t.Fatalf("unsubstituted parameter in rendered manifest:\n%s", r.Manifest)
			}
		})
	}
}
