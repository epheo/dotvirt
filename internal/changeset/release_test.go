package changeset

import (
	"errors"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

const declaredNS = `apiVersion: v1
kind: Namespace
metadata:
  name: alpha
  labels:
    dotvirt.io/project: legacy
`

const declaredNSWithUDN = declaredNS + `---
apiVersion: k8s.ovn.org/v1
kind: UserDefinedNetwork
metadata:
  name: default
  namespace: beta
`

func TestReleaseDeclaredSplitsGitFromResidue(t *testing.T) {
	// alpha is declared in the platform repo; ghost is label residue only.
	bare, _ := seedWork(t, map[string][]byte{"namespaces/alpha.yaml": []byte(declaredNS)})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	plat := project.ProjectInfo{Name: "platform", Repo: bare}
	target := project.ProjectInfo{Name: "legacy", Namespaces: []string{"alpha", "ghost"}}

	staged, residue, err := c.ReleaseDeclared(id, plat, target)
	if err != nil {
		t.Fatalf("ReleaseDeclared: %v", err)
	}
	if len(staged) != 1 || staged[0] != "alpha" {
		t.Fatalf("staged = %v, want [alpha]", staged)
	}
	if len(residue) != 1 || residue[0] != "ghost" {
		t.Fatalf("residue = %v, want [ghost]", residue)
	}

	// The staged rewrite keeps the file and drops the tenancy claim.
	view, err := c.Get(id, plat)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if view.Count != 1 {
		t.Fatalf("want 1 staged item, got %d", view.Count)
	}
	if y := view.Items[0].YAML; strings.Contains(y, "dotvirt.io/project") {
		t.Fatalf("rewrite still carries the project label:\n%s", y)
	} else if !strings.Contains(y, "name: alpha") {
		t.Fatalf("rewrite lost the namespace:\n%s", y)
	}
}

func TestReleaseDeclaredRefusesMultiDocManifest(t *testing.T) {
	// A namespace file carrying a UDN must refuse: rewriting it would hand
	// Argo a prune of tenant networking.
	bare, _ := seedWork(t, map[string][]byte{"namespaces/alpha.yaml": []byte(declaredNSWithUDN)})
	c := newTestCoordinator(t)
	plat := project.ProjectInfo{Name: "platform", Repo: bare}
	target := project.ProjectInfo{Name: "legacy", Namespaces: []string{"alpha"}}

	_, _, err := c.ReleaseDeclared(auth.Identity{Username: "alice"}, plat, target)
	if !errors.Is(err, model.ErrConflict) {
		t.Fatalf("want ErrConflict on a multi-doc manifest, got %v", err)
	}
}
