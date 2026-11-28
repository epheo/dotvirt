package changeset

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

func desktopsCoordinator(t *testing.T, files map[string][]byte) (*Coordinator, project.ProjectInfo) {
	t.Helper()
	bare, _ := seedWork(t, files)
	return newTestCoordinator(t, false).WithSyncNamespace("openshift-gitops"), project.ProjectInfo{Name: "platform", Repo: bare}
}

// namedGrantYAML is a grant other than ns's admin grant: the document
// RoleBindingManifest renders, under name.
func namedGrantYAML(ns, name, role string, subjects ...netgen.Subject) []byte {
	return []byte(strings.Replace(string(grantYAML(ns, role, subjects...)), "name: "+tenancy.AdminBinding(ns), "name: "+name, 1))
}

// Turning desktops on in a namespace grants, per persona: the project's
// sync account and its admins DeskuBe's writes; its admins alone the acts on
// anyone's claim; its authors alone the read of its desktops. Its viewers get
// nothing.
func TestStageDesktops(t *testing.T) {
	c, plat := desktopsCoordinator(t, map[string][]byte{
		"rbac/web.yaml":                 grantYAML("web", "admin", user("alice"), syncSA("shop")),
		"rbac/web/web-authors.yaml":     namedGrantYAML("web", "web-authors", "edit", user("carol"), syncSA("other")),
		"rbac/web/web-viewers.yaml":     namedGrantYAML("web", "web-viewers", "view", user("dave")),
		"rbac/web/web-egress-view.yaml": namedGrantYAML("web", "web-egress-view", tenancy.TenantEgressViewRole, user("carol")),
	})
	shop := project.ProjectInfo{Name: "shop", Namespaces: []string{"web"}}
	view, err := c.StageDesktops(auth.Identity{Username: "admin"}, plat, shop, "web")
	if err != nil {
		t.Fatalf("StageDesktops: %v", err)
	}
	items := stagedYAML(t, view)
	if got, want := slices.Sorted(maps.Keys(items)), []string{"web-desktops", "web-desktops-admin", "web-desktops-view"}; !slices.Equal(got, want) {
		t.Fatalf("staged %v, want %v", got, want)
	}
	for _, c := range []struct {
		name, role string
		want       []netgen.Subject
	}{
		{"web-desktops", tenancy.TenantDesktopsRole, []netgen.Subject{user("alice"), syncSA("shop")}},
		{"web-desktops-admin", tenancy.TenantDesktopsAdminRole, []netgen.Subject{user("alice")}},
		{"web-desktops-view", tenancy.TenantDesktopsViewRole, []netgen.Subject{user("carol")}},
	} {
		role, subjects := grantSubjects(t, items[c.name], c.name)
		if role != c.role || !slices.Equal(subjects, c.want) {
			t.Errorf("%s = %s %+v, want %s %+v", c.name, role, subjects, c.role, c.want)
		}
	}
	entries, err := c.store.List("admin", plat.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Resource != draft.ResourceRoleBinding || e.SourceFile != "rbac/web/"+e.Name+".yaml" {
			t.Errorf("%s staged as %s at %s", e.Name, e.Resource, e.SourceFile)
		}
	}
}

// Staged as git holds it, nothing is left; a person granted admin since gains
// the desktop grants, each keeping who it already names.
func TestDesktopsOnceInGit(t *testing.T) {
	grant := func(render func(netgen.RoleBindingSpec) (string, []byte, error), spec netgen.RoleBindingSpec) (string, []byte) {
		path, b, err := render(spec)
		if err != nil {
			t.Fatal(err)
		}
		return path, b
	}
	writesPath, writes := grant(netgen.DesktopsManifest, netgen.RoleBindingSpec{Namespace: "web", Project: "shop", Kept: []netgen.Subject{user("alice")}, SyncAccount: ptr(syncSA("shop"))})
	claimsPath, claims := grant(netgen.DesktopsAdminManifest, netgen.RoleBindingSpec{Namespace: "web", Project: "shop", Kept: []netgen.Subject{user("alice")}})
	seed := map[string][]byte{
		"rbac/web.yaml": grantYAML("web", "admin", user("alice"), syncSA("shop")),
		writesPath:      writes,
		claimsPath:      claims,
	}
	c, plat := desktopsCoordinator(t, seed)
	shop := project.ProjectInfo{Name: "shop", Namespaces: []string{"web"}}
	on, _, err := c.DesktopsNeeds(plat, shop, "web")
	if !errors.Is(err, ErrDesktopsOn) || !on {
		t.Fatalf("all held: on=%v %v, want on, ErrDesktopsOn", on, err)
	}

	seed["rbac/web.yaml"] = grantYAML("web", "admin", user("alice"), user("bob"), syncSA("shop"))
	c, plat = desktopsCoordinator(t, seed)
	view, err := c.StageDesktops(auth.Identity{Username: "admin"}, plat, shop, "web")
	if err != nil {
		t.Fatalf("a new admin: %v", err)
	}
	items := stagedYAML(t, view)
	if _, subjects := grantSubjects(t, items["web-desktops"], "web-desktops"); !slices.Equal(subjects, []netgen.Subject{user("alice"), user("bob"), syncSA("shop")}) {
		t.Errorf("writes name %+v", subjects)
	}
	if _, subjects := grantSubjects(t, items["web-desktops-admin"], "web-desktops-admin"); !slices.Equal(subjects, []netgen.Subject{user("alice"), user("bob")}) {
		t.Errorf("claim acts name %+v", subjects)
	}
}

// The desktop grants follow the admin grant as the egress grant does:
// granting the sync account later rewrites the writes, never the claim acts.
func TestDesktopsFollowTheAdminGrant(t *testing.T) {
	writesPath, writes, err := netgen.DesktopsManifest(netgen.RoleBindingSpec{Namespace: "web", Kept: []netgen.Subject{user("alice")}})
	if err != nil {
		t.Fatal(err)
	}
	c, plat := desktopsCoordinator(t, map[string][]byte{
		"rbac/web.yaml": grantYAML("web", "admin", user("alice")),
		writesPath:      writes,
	})
	view, err := c.StageSyncAccess(auth.Identity{Username: "admin"}, plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"web"}})
	if err != nil {
		t.Fatalf("StageSyncAccess: %v", err)
	}
	items := stagedYAML(t, view)
	_, admin := grantSubjects(t, items["web-admins"], "web-admins")
	if _, got := grantSubjects(t, items["web-desktops"], "web-desktops"); !slices.Equal(got, admin) {
		t.Errorf("desktop writes name %+v, want the admin grant's %+v", got, admin)
	}
	if _, ok := items["web-desktops-admin"]; ok {
		t.Error("the claim acts staged where git declares none")
	}
}

// Refused: a namespace with no admin-role grant to follow, and one outside
// the project.
func TestDesktopsRefusals(t *testing.T) {
	c, plat := desktopsCoordinator(t, map[string][]byte{
		"rbac/db.yaml": grantYAML("db", "edit", user("bob")),
	})
	shop := project.ProjectInfo{Name: "shop", Namespaces: []string{"web", "db"}}
	for _, ns := range []string{"web", "db"} {
		if _, _, err := c.DesktopsNeeds(plat, shop, ns); !errors.Is(err, ErrDesktopsUngranted) {
			t.Errorf("%s: %v, want ErrDesktopsUngranted", ns, err)
		}
	}
	if _, _, err := c.DesktopsNeeds(plat, shop, "elsewhere"); err == nil {
		t.Error("a namespace outside the project was planned")
	}
}

// Releasing the project takes its sync account out of the desktop writes too.
func TestReleaseRevokesDesktopWrites(t *testing.T) {
	writesPath, writes, err := netgen.DesktopsManifest(netgen.RoleBindingSpec{Namespace: "web", Kept: []netgen.Subject{user("alice")}, SyncAccount: ptr(syncSA("legacy"))})
	if err != nil {
		t.Fatal(err)
	}
	c, plat := desktopsCoordinator(t, map[string][]byte{
		"rbac/web.yaml": grantYAML("web", "admin", user("alice"), syncSA("legacy")),
		writesPath:      writes,
	})
	id := auth.Identity{Username: "admin"}
	if _, _, err := c.ReleaseDeclared(id, plat, project.ProjectInfo{Name: "legacy", Namespaces: []string{"web"}}); err != nil {
		t.Fatalf("ReleaseDeclared: %v", err)
	}
	d, err := c.store.List(id.Username, plat.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range d {
		if e.Name == "web-desktops" {
			if _, subjects := grantSubjects(t, e.Manifest, e.Name); !slices.Equal(subjects, []netgen.Subject{user("alice")}) {
				t.Errorf("desktop writes after release = %+v, want alice alone", subjects)
			}
			return
		}
	}
	t.Error("the desktop writes kept the released project's sync account")
}
