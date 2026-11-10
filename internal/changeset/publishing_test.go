package changeset

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/publish"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

var pubSettings = publish.Settings{Domain: "apps.example.com", GatewayClass: "portail", Issuer: "letsencrypt", ProjectLabel: "dotvirt.io/project"}

func publishingCoordinator(t *testing.T, files map[string][]byte) (*Coordinator, project.ProjectInfo) {
	t.Helper()
	bare, _ := seedWork(t, files)
	c := newTestCoordinator(t, false).WithSyncNamespace("openshift-gitops").WithPublishing(pubSettings)
	return c, project.ProjectInfo{Name: "platform", Repo: bare}
}

func stagedYAML(t *testing.T, view model.DraftView) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, it := range view.Items {
		out[it.Name] = it.YAML
	}
	return out
}

// on is every namespace on the cluster's own network but those named.
func on(networks map[string]string) PublishTarget { return PublishTarget{Networks: networks} }

// The project's Gateway goes in the first namespace of its network, takes
// routes from every namespace labeled with the project, and each namespace it
// reaches gets route writes for its admin grant's subjects and the project's
// sync account.
func TestStagePublishing(t *testing.T) {
	c, plat := publishingCoordinator(t, map[string][]byte{
		"rbac/web.yaml": grantYAML("web", "admin", user("alice")),
		"rbac/db.yaml":  grantYAML("db", "admin", user("bob")),
	})
	shop := project.ProjectInfo{Name: "shop", Namespaces: []string{"web", "db"}}
	view, err := c.StagePublishing(auth.Identity{Username: "admin"}, plat, shop, on(nil))
	if err != nil {
		t.Fatalf("StagePublishing: %v", err)
	}
	items := stagedYAML(t, view)
	if want := []string{"db-routes", publish.Gateway, publish.Certificate, "web-routes"}; !slices.Equal(slices.Sorted(maps.Keys(items)), want) {
		t.Fatalf("staged %v, want %v", slices.Sorted(maps.Keys(items)), want)
	}
	gw := items[publish.Gateway]
	for _, want := range []string{"namespace: db", "'*.shop.apps.example.com'", "from: Selector", "dotvirt.io/project: shop", "kind: HTTPRoute", "name: publish-tls"} {
		if !strings.Contains(gw, want) {
			t.Errorf("Gateway lacks %q:\n%s", want, gw)
		}
	}
	role, subjects := grantSubjects(t, items["web-routes"], "web-routes")
	if role != tenancy.TenantRoutesRole || !slices.Equal(subjects, []netgen.Subject{user("alice"), syncSA("shop")}) {
		t.Errorf("web route grant = %s %+v, want %s to alice and the sync account", role, subjects, tenancy.TenantRoutesRole)
	}
}

// The Gateway serves the namespaces on its network: it goes in the largest
// one, a namespace with a VM Network of its own is not reached, and where git
// holds the Gateway already, there it stays.
func TestPublishingPlan(t *testing.T) {
	c, plat := publishingCoordinator(t, map[string][]byte{
		"publishing/c/publish.yaml": []byte("apiVersion: gateway.networking.k8s.io/v1\nkind: Gateway\nmetadata:\n  name: publish\n  namespace: c\n"),
	})
	shop := project.ProjectInfo{Name: "shop", Namespaces: []string{"a", "b", "c"}}
	split := map[string]string{"a": "udn:a/own"}

	plan, _, err := c.PublishingNeeds(plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"a", "b", "d"}}, on(split))
	if err != nil || plan.Namespace != "b" || !slices.Equal(plan.Reach, []string{"b", "d"}) || plan.On || plan.Primary {
		t.Errorf("largest network: %+v %v, want b reaching b and d", plan, err)
	}
	plan, _, _ = c.PublishingNeeds(plat, shop, on(split))
	if plan.Namespace != "c" || !plan.On || !slices.Equal(plan.Reach, []string{"b", "c"}) {
		t.Errorf("held in git: %+v, want c, on, reaching b and c", plan)
	}
	plan, need, err := c.PublishingNeeds(plat, project.ProjectInfo{Name: "vpc", Namespaces: []string{"x", "y"}}, on(map[string]string{"x": "cudn:vpc", "y": "cudn:vpc"}))
	if !errors.Is(err, ErrPrimaryNetwork) || !plan.Primary || !slices.Equal(plan.Reach, []string{"x", "y"}) || len(need) == 0 {
		t.Errorf("VM Network: %+v %d accesses %v, want primary, both reached, the accesses it would ask", plan, len(need), err)
	}
}

// Two Gateways in one project, a project without a namespace, and an install
// without publishing are refused; where the admin role writes HTTPRoutes
// already, no route grant is staged.
func TestPublishingRefusals(t *testing.T) {
	gw := func(ns string) []byte {
		return []byte("apiVersion: gateway.networking.k8s.io/v1\nkind: Gateway\nmetadata:\n  name: publish\n  namespace: " + ns + "\n")
	}
	c, plat := publishingCoordinator(t, map[string][]byte{
		"publishing/a/publish.yaml": gw("a"),
		"publishing/b/publish.yaml": gw("b"),
		"rbac/web.yaml":             grantYAML("web", "admin", user("alice")),
	})
	if _, _, err := c.PublishingNeeds(plat, project.ProjectInfo{Name: "two", Namespaces: []string{"a", "b"}}, on(nil)); !errors.Is(err, model.ErrConflict) {
		t.Errorf("two Gateways: %v, want ErrConflict", err)
	}
	if _, _, err := c.PublishingNeeds(plat, project.ProjectInfo{Name: "empty"}, on(nil)); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("no namespace: %v, want ErrInvalid", err)
	}
	_, need, err := c.PublishingNeeds(plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"web"}}, PublishTarget{RoutesGranted: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range need {
		if a.Resource == "rolebindings" {
			t.Errorf("admin writes routes, yet the change asks %s", a)
		}
	}
	off := newTestCoordinator(t, false)
	if _, _, err := off.PublishingNeeds(plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"web"}}, on(nil)); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("publishing off: %v, want ErrUnavailable", err)
	}
}

// Once git holds the Gateway, the certificate and each reached namespace's
// route grant, nothing is left to stage; a namespace that joined since
// lacks its grant, and only that grant is staged.
func TestPublishingOnceInGit(t *testing.T) {
	files, err := publish.Manifests(pubSettings, "shop", "db")
	if err != nil {
		t.Fatal(err)
	}
	routes := func(ns, admin string) []byte {
		_, b, err := netgen.RoutesManifest(netgen.RoleBindingSpec{Namespace: ns, Project: "shop", Kept: []netgen.Subject{user(admin)}, SyncAccount: ptr(syncSA("shop"))})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	seed := map[string][]byte{
		"rbac/db.yaml":           grantYAML("db", "admin", user("bob")),
		"rbac/db/db-routes.yaml": routes("db", "bob"),
		"rbac/web.yaml":          grantYAML("web", "admin", user("alice")),
	}
	for _, f := range files {
		seed[f.Path] = f.Content
	}
	c, plat := publishingCoordinator(t, seed)
	plan, _, err := c.PublishingNeeds(plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"db"}}, on(nil))
	if !errors.Is(err, ErrPublishingOn) || !plan.On {
		t.Errorf("all held: %+v %v, want on, ErrPublishingOn", plan, err)
	}
	view, err := c.StagePublishing(auth.Identity{Username: "admin"}, plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"db", "web"}}, on(nil))
	if err != nil {
		t.Fatalf("a joined namespace: %v", err)
	}
	if got := slices.Sorted(maps.Keys(stagedYAML(t, view))); !slices.Equal(got, []string{"web-routes"}) {
		t.Errorf("staged %v, want web's route grant alone", got)
	}
}

// The route grant follows the admin grant as the egress grant does: granting
// the project's sync account later rewrites both.
func TestRoutesGrantFollowsTheAdminGrant(t *testing.T) {
	_, routes, err := netgen.RoutesManifest(netgen.RoleBindingSpec{Namespace: "web", Kept: []netgen.Subject{user("alice")}})
	if err != nil {
		t.Fatal(err)
	}
	c, plat := publishingCoordinator(t, map[string][]byte{
		"rbac/web.yaml":            grantYAML("web", "admin", user("alice")),
		"rbac/web/web-routes.yaml": routes,
	})
	view, err := c.StageSyncAccess(auth.Identity{Username: "admin"}, plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"web"}})
	if err != nil {
		t.Fatalf("StageSyncAccess: %v", err)
	}
	items := stagedYAML(t, view)
	_, admin := grantSubjects(t, items["web-admins"], "web-admins")
	if _, got := grantSubjects(t, items["web-routes"], "web-routes"); !slices.Equal(got, admin) {
		t.Errorf("route grant names %+v, want the admin grant's %+v", got, admin)
	}
}
