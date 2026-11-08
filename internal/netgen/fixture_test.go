package netgen

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the operator's grant fixture")

// grantFixture is the operator envtest's copy of one project namespace's
// grants, as this package renders them for namespace x-ns of project
// x-project, owned by x-project-owner, synced from the argocd namespace. The
// operator module cannot import this one, so it reads them from here and
// substitutes its names.
var grantFixture = filepath.Join("..", "..", "operator", "internal", "controller", "testdata", "project-grants.yaml")

func TestGrantFixture(t *testing.T) {
	spec := RoleBindingSpec{Namespace: "x-ns", Project: "x-project", Owners: []string{"x-project-owner"}, SyncAccount: ServiceAccountSubject("argocd", "dotvirt-p-x-project")}
	_, admin, err := RoleBindingManifest(spec)
	if err != nil {
		t.Fatal(err)
	}
	_, egress, err := EgressManifest(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(admin, "---\n"...), egress...)
	if *update {
		if err := os.WriteFile(grantFixture, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(grantFixture)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s is stale: go test ./internal/netgen -run TestGrantFixture -update", grantFixture)
	}
}
