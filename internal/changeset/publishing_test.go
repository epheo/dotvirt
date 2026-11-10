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

func publishingCoordinator(t *testing.T, files map[string][]byte) (*Coordinator, project.ProjectInfo) {
	t.Helper()
	bare, _ := seedWork(t, files)
	c := newTestCoordinator(t, false).WithSyncNamespace("openshift-gitops").
		WithPublishing(publish.Settings{Domain: "apps.example.com", GatewayClass: "portail", Issuer: "letsencrypt"})
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

// Publishing stages the namespace's Gateway and certificate, and route writes
// for its admin grant's subjects and the project's sync account. Staged
// again once on, it stages nothing.
func TestStagePublishing(t *testing.T) {
	c, plat := publishingCoordinator(t, map[string][]byte{
		"rbac/web.yaml": grantYAML("web", "admin", user("alice")),
	})
	id := auth.Identity{Username: "admin"}
	shop := project.ProjectInfo{Name: "shop", Namespaces: []string{"web", "db"}}
	view, err := c.StagePublishing(id, plat, shop, PublishTarget{Namespace: "web"})
	if err != nil {
		t.Fatalf("StagePublishing: %v", err)
	}
	items := stagedYAML(t, view)
	if want := []string{publish.Gateway, publish.Certificate, "web-routes"}; !slices.Equal(slices.Sorted(maps.Keys(items)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("staged %v, want %v", slices.Sorted(maps.Keys(items)), want)
	}
	gw := items[publish.Gateway]
	for _, want := range []string{"'*.web.apps.example.com'", "from: Same", "kind: HTTPRoute", "gatewayClassName: portail", "name: publish-tls"} {
		if !strings.Contains(gw, want) {
			t.Errorf("Gateway lacks %q:\n%s", want, gw)
		}
	}
	role, subjects := grantSubjects(t, items["web-routes"], "web-routes")
	if role != tenancy.TenantRoutesRole || !slices.Equal(subjects, []netgen.Subject{user("alice"), syncSA("shop")}) {
		t.Errorf("route grant = %s %+v, want %s to alice and the sync account", role, subjects, tenancy.TenantRoutesRole)
	}
}

// A namespace with its own VM Network waits on portail; one outside the
// project, or an install without publishing, is refused; where the admin role
// writes HTTPRoutes already, no route grant is staged.
func TestPublishingRefusals(t *testing.T) {
	files := map[string][]byte{"rbac/web.yaml": grantYAML("web", "admin", user("alice"))}
	shop := project.ProjectInfo{Name: "shop", Namespaces: []string{"web"}}
	c, plat := publishingCoordinator(t, files)
	if _, err := c.PublishingNeeds(plat, shop, PublishTarget{Namespace: "web", PrimaryNetwork: true}); !errors.Is(err, ErrPrimaryNetwork) {
		t.Errorf("primary network: %v, want ErrPrimaryNetwork", err)
	}
	if _, err := c.PublishingNeeds(plat, shop, PublishTarget{Namespace: "elsewhere"}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("namespace outside the project: %v, want ErrInvalid", err)
	}
	need, err := c.PublishingNeeds(plat, shop, PublishTarget{Namespace: "web", RoutesGranted: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range need {
		if a.Resource == "rolebindings" {
			t.Errorf("admin writes routes, yet the change asks %s", a)
		}
	}
	off := newTestCoordinator(t, false)
	if _, err := off.PublishingNeeds(plat, shop, PublishTarget{Namespace: "web"}); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("publishing off: %v, want ErrUnavailable", err)
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
