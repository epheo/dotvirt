package changeset

import (
	"errors"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// One file that does not parse costs its own VMs, not the project's: the rest
// still list (a quoted kind included, the library excluded), the result is
// served from the memo, and adopting the unparsed VM is refused rather than
// staged as a second declaration beside the broken file.
func TestVMIndexSurvivesUnparsedFile(t *testing.T) {
	bare := seedBareFiles(t, map[string][]byte{
		"alpha/web.yaml":     []byte(liveVMYAML("web", "")),
		"alpha/quoted.yaml":  []byte("apiVersion: kubevirt.io/v1\nkind: \"VirtualMachine\"\nmetadata:\n  name: quoted\n"),
		"alpha/bad.yaml":     []byte(liveVMYAML("bad", "") + "---\nfoo: [\n"),
		"templates/raw.yaml": []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: raw\n  namespace: alpha\n"),
	})
	c := newTestCoordinator(t, false)
	c.live = fakeLive{"alpha/bad.yaml": liveVMYAML("bad", "")}
	proj := project.ProjectInfo{Name: "p", Repo: bare}
	read := mustRead(t, c, proj)

	vms, err := VMsOnBranch(read, "main")
	if err != nil {
		t.Fatalf("VMsOnBranch: %v", err)
	}
	var names []string
	for _, vm := range vms {
		names = append(names, vm.Namespace+"/"+vm.Name)
	}
	if len(names) != 2 || names[0] != "alpha/quoted" || names[1] != "alpha/web" {
		t.Fatalf("VMs = %v, want [alpha/quoted alpha/web]", names)
	}
	again, err := VMsOnBranch(read, "main")
	if err != nil || &again[0] != &vms[0] {
		t.Errorf("second read must be served from the memo (err %v)", err)
	}

	id := auth.Identity{Username: "alice"}
	if _, err := c.Adopt(id, proj, "alpha", "bad"); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("adopting a VM declared in an unparsed file: want ErrConflict, got %v", err)
	}
	if entries, _ := c.store.List(id.Username, proj.Name); len(entries) != 0 {
		t.Errorf("refused adopt staged %+v", entries)
	}
}
