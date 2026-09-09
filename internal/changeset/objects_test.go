package changeset

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
)

// seedNetworkRepo commits one project network (alpha/db-net) plus a namespace
// file that also declares its primary network, at the paths netgen renders.
func seedNetworkRepo(t *testing.T) string {
	t.Helper()
	path, content, err := netgen.Manifest(netgen.Spec{Name: "db-net", Scope: netgen.ScopeProject, Namespace: "alpha", Subnets: []string{"10.20.0.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	nsPath, nsContent, err := netgen.NamespaceManifest(netgen.NamespaceSpec{Name: "alpha", Project: "p", Repo: "r", VMNetwork: &netgen.PrimaryNet{Name: "vm-net", Subnet: "10.30.0.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	return seedBareFiles(t, map[string][]byte{path: content, nsPath: nsContent})
}

// Re-submitting the create form for a network git already declares stages an
// edit of the declaring file, not a second create.
func TestStageCreateOfDeclaredObjectIsEdit(t *testing.T) {
	bare := seedNetworkRepo(t)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	raw, _ := json.Marshal(netgen.Spec{Name: "db-net", Scope: netgen.ScopeProject, Namespace: "alpha", Subnets: []string{"10.20.0.0/23"}})
	view, err := c.StageCreateNetwork(id, proj, raw)
	if err != nil {
		t.Fatalf("StageCreateNetwork: %v", err)
	}
	if len(view.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", view.Items)
	}
	it := view.Items[0]
	if it.Kind != string(draft.KindEdit) || it.Resource != string(draft.ResourceNetwork) {
		t.Fatalf("want a network edit, got %+v", it)
	}
	if !strings.Contains(it.YAML, "10.20.0.0/23") || !strings.Contains(it.BaseYAML, "10.20.0.0/24") {
		t.Errorf("edit should carry new and base YAML, got yaml=%q base=%q", it.YAML, it.BaseYAML)
	}
	if len(it.Changes) != 1 || it.Changes[0].Field != "Edit network" {
		t.Errorf("want an Edit network change, got %+v", it.Changes)
	}

	raw, _ = json.Marshal(netgen.Spec{Name: "new-net", Scope: netgen.ScopeProject, Namespace: "alpha"})
	view, err = c.StageCreateNetwork(id, proj, raw)
	if err != nil {
		t.Fatalf("StageCreateNetwork: %v", err)
	}
	if got := view.Items[len(view.Items)-1]; got.Kind != string(draft.KindCreate) || got.Name != "new-net" {
		t.Errorf("an undeclared network stays a create, got %+v", got)
	}
}

// Deleting a declared network stages its file's removal; a VM delete rides the
// same path. An object sharing its file with others is refused, never silently
// removed with them.
func TestStageDeleteObjects(t *testing.T) {
	bare := seedNetworkRepo(t)
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	view, err := c.StageDelete(id, proj, string(draft.ResourceNetwork), "alpha", "db-net")
	if err != nil {
		t.Fatalf("StageDelete: %v", err)
	}
	it := view.Items[0]
	if it.Kind != string(draft.KindDelete) || it.Resource != string(draft.ResourceNetwork) {
		t.Fatalf("unexpected item: %+v", it)
	}
	if it.Changes[0].From != "alpha/db-net" {
		t.Errorf("remove change should name the object, got %+v", it.Changes)
	}
	entries, _ := c.store.List(id.Username, proj.Name)
	if entries[0].SourceFile != "alpha/networks/db-net.yaml" {
		t.Errorf("delete should capture the declaring file, got %q", entries[0].SourceFile)
	}

	_, err = c.StageDelete(id, proj, string(draft.ResourceNetwork), "alpha", "vm-net")
	if !errors.Is(err, model.ErrConflict) {
		t.Errorf("a network declared beside its namespace must be refused with ErrConflict, got %v", err)
	}
	_, err = c.StageDelete(id, proj, string(draft.ResourceNetwork), "alpha", "ghost")
	if !errors.Is(err, model.ErrNotFound) {
		t.Errorf("want ErrNotFound for an undeclared network, got %v", err)
	}
}

// ObjectSpec hands the edit form the spec that rendered the declared file.
func TestObjectSpec(t *testing.T) {
	bare := seedNetworkRepo(t)
	c := newTestCoordinator(t)
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	got, err := c.ObjectSpec(proj, string(draft.ResourceNetwork), "alpha", "db-net")
	if err != nil {
		t.Fatalf("ObjectSpec: %v", err)
	}
	if got.SourceFile != "alpha/networks/db-net.yaml" {
		t.Errorf("sourceFile = %q", got.SourceFile)
	}
	var spec netgen.Spec
	if err := json.Unmarshal(got.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Name != "db-net" || spec.Namespace != "alpha" || len(spec.Subnets) != 1 {
		t.Errorf("spec = %+v", spec)
	}
}
