package changeset

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
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

// Turning desktops on in a namespace grants DeskuBe's pool writes and claim
// verbs to the project's sync account and its admins, and to nobody else: not
// its authors nor its viewers.
func TestStageDesktops(t *testing.T) {
	c, plat := desktopsCoordinator(t, map[string][]byte{
		"rbac/web.yaml":             grantYAML("web", "admin", user("alice"), syncSA("shop")),
		"rbac/web/web-authors.yaml": namedGrantYAML("web", "web-authors", "edit", user("carol")),
		"rbac/web/web-viewers.yaml": namedGrantYAML("web", "web-viewers", "view", user("dave")),
	})
	shop := project.ProjectInfo{Name: "shop", Namespaces: []string{"web"}}
	view, err := c.StageDesktops(auth.Identity{Username: "admin"}, plat, shop, "web")
	if err != nil {
		t.Fatalf("StageDesktops: %v", err)
	}
	items := stagedYAML(t, view)
	if got := slices.Sorted(maps.Keys(items)); !slices.Equal(got, []string{"web-desktops"}) {
		t.Fatalf("staged %v, want web-desktops alone", got)
	}
	role, subjects := grantSubjects(t, items["web-desktops"], "web-desktops")
	if role != tenancy.TenantDesktopsRole || !slices.Equal(subjects, []netgen.Subject{user("alice"), syncSA("shop")}) {
		t.Errorf("web-desktops = %s %+v, want %s to alice and the sync account", role, subjects, tenancy.TenantDesktopsRole)
	}
	entries, err := c.store.List("admin", plat.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Resource != draft.ResourceRoleBinding || entries[0].SourceFile != "rbac/web/web-desktops.yaml" {
		t.Errorf("staged %+v", entries)
	}
}

// Staged as git holds it, nothing is left; a person granted admin since gains
// the grant, which keeps who it already names.
func TestDesktopsOnceInGit(t *testing.T) {
	path, writes, err := netgen.DesktopsManifest(netgen.RoleBindingSpec{Namespace: "web", Project: "shop", Kept: []netgen.Subject{user("alice")}, SyncAccount: ptr(syncSA("shop"))})
	if err != nil {
		t.Fatal(err)
	}
	seed := map[string][]byte{
		"rbac/web.yaml": grantYAML("web", "admin", user("alice"), syncSA("shop")),
		path:            writes,
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
	if _, subjects := grantSubjects(t, stagedYAML(t, view)["web-desktops"], "web-desktops"); !slices.Equal(subjects, []netgen.Subject{user("alice"), user("bob"), syncSA("shop")}) {
		t.Errorf("grant names %+v", subjects)
	}
}

// The desktop grant follows the admin grant as the egress grant does:
// granting the sync account later rewrites it.
func TestDesktopsFollowTheAdminGrant(t *testing.T) {
	path, writes, err := netgen.DesktopsManifest(netgen.RoleBindingSpec{Namespace: "web", Kept: []netgen.Subject{user("alice")}})
	if err != nil {
		t.Fatal(err)
	}
	c, plat := desktopsCoordinator(t, map[string][]byte{
		"rbac/web.yaml": grantYAML("web", "admin", user("alice")),
		path:            writes,
	})
	view, err := c.StageSyncAccess(auth.Identity{Username: "admin"}, plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"web"}})
	if err != nil {
		t.Fatalf("StageSyncAccess: %v", err)
	}
	items := stagedYAML(t, view)
	_, admin := grantSubjects(t, items["web-admins"], "web-admins")
	if _, got := grantSubjects(t, items["web-desktops"], "web-desktops"); !slices.Equal(got, admin) {
		t.Errorf("desktop grant names %+v, want the admin grant's %+v", got, admin)
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
	if _, _, err := c.DesktopsNeeds(plat, shop, "elsewhere"); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("a namespace outside the project: %v", err)
	}
}

// Releasing the project takes its sync account out of the desktop grant too.
func TestReleaseRevokesDesktopWrites(t *testing.T) {
	path, writes, err := netgen.DesktopsManifest(netgen.RoleBindingSpec{Namespace: "web", Kept: []netgen.Subject{user("alice")}, SyncAccount: ptr(syncSA("legacy"))})
	if err != nil {
		t.Fatal(err)
	}
	c, plat := desktopsCoordinator(t, map[string][]byte{
		"rbac/web.yaml": grantYAML("web", "admin", user("alice"), syncSA("legacy")),
		path:            writes,
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
				t.Errorf("desktop grant after release = %+v, want alice alone", subjects)
			}
			return
		}
	}
	t.Error("the desktop grant kept the released project's sync account")
}

// A tenant repo declares a pool only while Argo syncs it as the project's own
// account: until then staging refuses it, naming the step that lets it.
func TestDesktopPoolsWaitOnImpersonation(t *testing.T) {
	proj := project.ProjectInfo{Name: "shop"}
	err := tenantDeclarable(KindRule{}, proj, "vdi.desku.be", "VdiPool", "design")
	if !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "spec.argocd.syncImpersonation") {
		t.Errorf("not impersonating: %v", err)
	}
	on := KindRule{Policy: tenancy.Policy{Allowed: []tenancy.GroupKind{{Group: "vdi.desku.be", Kind: "VdiPool"}}}}
	if err := tenantDeclarable(on, proj, "vdi.desku.be", "VdiPool", "design"); err != nil {
		t.Errorf("impersonating: %v", err)
	}
	if err := tenantDeclarable(on, proj, "vdi.desku.be", "VdiInstance", "desk"); err == nil || strings.Contains(err.Error(), "this install's tenant repos") {
		t.Errorf("a desktop: %v, want the platform repo's alone", err)
	}
}
