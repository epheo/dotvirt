package git

import (
	"context"
	"testing"
	"time"
)

// The templates/ dir is the library: TemplatesOnBranch reads exactly it, and
// VMManifests must skip it — a template's embedded VM blueprint would otherwise
// surface as inventory (it passes the cheap kind pre-filter).
func TestTemplatesDirSplit(t *testing.T) {
	bare := seedMultiRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := NewRepoSet(ctx, "", nil, true, nil, time.Hour)
	read, write, err := repos.Get(bare)
	if err != nil {
		t.Fatal(err)
	}
	tpl := "apiVersion: template.kubevirt.io/v1beta1\nkind: VirtualMachineTemplate\nmetadata:\n  name: base\nspec:\n  virtualMachine:\n    kind: VirtualMachine\n"
	if _, err := write.Commit("seed", "add template", []File{
		{Path: "templates/base.yaml", Content: []byte(tpl)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := read.refresh(); err != nil {
		t.Fatal(err)
	}

	tpls, err := read.TemplatesOnBranch("seed")
	if err != nil {
		t.Fatal(err)
	}
	if len(tpls) != 1 || tpls[0].Path != "templates/base.yaml" {
		t.Fatalf("TemplatesOnBranch: %+v", tpls)
	}

	vms, err := read.VMManifests("seed")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range vms {
		if inTemplatesDir(f.Path) {
			t.Fatalf("VMManifests leaked a library file: %s", f.Path)
		}
	}
	if len(vms) != 2 {
		t.Fatalf("want the 2 seeded VM manifests, got %d", len(vms))
	}
}
