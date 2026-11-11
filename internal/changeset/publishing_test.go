package changeset

import (
	"encoding/json"
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

var pubSettings = publish.Settings{Domain: "apps.example.com", GatewayClass: "portail", Issuer: "letsencrypt", ProjectLabel: "dotvirt.io/project", PortailNamespace: "portail-system"}

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

// on is a project whose publish namespace the cluster holds, every namespace
// on the cluster's own network but those named.
func on(networks map[string]string) PublishTarget {
	return PublishTarget{PublishNamespace: true, Networks: networks}
}

// The project's Gateway goes in its publish namespace, takes routes from
// every namespace labeled with the project, and each namespace it reaches
// gets route writes for its admin grant's subjects and the project's sync
// account; those admins, and not the sync account, may read the Gateway.
func TestStagePublishing(t *testing.T) {
	c, plat := publishingCoordinator(t, map[string][]byte{
		"rbac/web.yaml": grantYAML("web", "admin", user("alice"), syncSA("shop")),
		"rbac/db.yaml":  grantYAML("db", "admin", user("bob")),
	})
	shop := project.ProjectInfo{Name: "shop", Namespaces: []string{"web", "db"}}
	view, err := c.StagePublishing(auth.Identity{Username: "admin"}, plat, shop, on(nil))
	if err != nil {
		t.Fatalf("StagePublishing: %v", err)
	}
	items := stagedYAML(t, view)
	if want := []string{"db-routes", publish.Gateway, publish.Certificate, tenancy.GatewayViewBinding, "web-routes"}; !slices.Equal(slices.Sorted(maps.Keys(items)), want) {
		t.Fatalf("staged %v, want %v", slices.Sorted(maps.Keys(items)), want)
	}
	gw := items[publish.Gateway]
	for _, want := range []string{"namespace: shop-publish", "'*.shop.apps.example.com'", "from: Selector", "dotvirt.io/project: shop", "kind: HTTPRoute", "name: publish-tls"} {
		if !strings.Contains(gw, want) {
			t.Errorf("Gateway lacks %q:\n%s", want, gw)
		}
	}
	role, subjects := grantSubjects(t, items["web-routes"], "web-routes")
	if role != tenancy.TenantRoutesRole || !slices.Equal(subjects, []netgen.Subject{user("alice"), syncSA("shop")}) {
		t.Errorf("web route grant = %s %+v, want %s to alice and the sync account", role, subjects, tenancy.TenantRoutesRole)
	}
	role, subjects = grantSubjects(t, items[tenancy.GatewayViewBinding], tenancy.GatewayViewBinding)
	if role != tenancy.TenantGatewayViewRole || !slices.Equal(subjects, []netgen.Subject{user("bob"), user("alice")}) || !strings.Contains(items[tenancy.GatewayViewBinding], "namespace: shop-publish") {
		t.Errorf("gateway view = %s %+v, want %s in shop-publish to bob and alice alone", role, subjects, tenancy.TenantGatewayViewRole)
	}
}

// The Gateway serves the project's namespaces on its publish namespace's
// network: one with a VM Network of its own is not reached, and a project
// none of whose namespaces shares it is refused.
func TestPublishingPlan(t *testing.T) {
	c, plat := publishingCoordinator(t, map[string][]byte{
		"publishing/shop-publish/publish.yaml": []byte("apiVersion: gateway.networking.k8s.io/v1\nkind: Gateway\nmetadata:\n  name: publish\n  namespace: shop-publish\n"),
	})
	plan, _, err := c.PublishingNeeds(plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"a", "b", "d"}}, on(map[string]string{"a": "udn:a/own"}))
	if err != nil || plan.Namespace != "shop-publish" || !slices.Equal(plan.Reach, []string{"b", "d"}) || !plan.On || plan.Primary {
		t.Errorf("shop: %+v %v, want on in shop-publish, reaching b and d", plan, err)
	}
	if _, _, err := c.PublishingNeeds(plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"a"}}, on(map[string]string{"a": "udn:a/own"})); !errors.Is(err, ErrApart) {
		t.Errorf("no namespace on the publish namespace's network: %v, want ErrApart", err)
	}
	vpc := project.ProjectInfo{Name: "vpc", Namespaces: []string{"x", "y", "z"}}
	cudn := on(map[string]string{"x": "cudn:vpc", "y": "cudn:vpc", "vpc-publish": "cudn:vpc"})
	plan, need, err := c.PublishingNeeds(plat, vpc, cudn)
	if !errors.Is(err, ErrPrimaryNetwork) || !plan.Primary || !slices.Equal(plan.Reach, []string{"x", "y"}) || len(need) == 0 {
		t.Errorf("VM Network: %+v %d accesses %v, want primary, x and y reached, the accesses it would ask", plan, len(need), err)
	}
	cudn.Isolated = true
	view, err := c.StagePublishing(auth.Identity{Username: "admin"}, plat, vpc, cudn)
	if err != nil {
		t.Fatalf("VM Network, Gateways isolated: %v", err)
	}
	var proxied []string
	for _, it := range view.Items {
		if it.Name == publish.Parameters || it.Name == publish.ProxyBinding("vpc") {
			proxied = append(proxied, it.Namespace+"/"+it.Name)
			if it.Name == publish.ProxyBinding("vpc") && !strings.Contains(it.YAML, "namespace: vpc-publish") {
				t.Errorf("the proxy binding names no account in vpc-publish:\n%s", it.YAML)
			}
		}
	}
	if want := []string{"vpc-publish/" + publish.Parameters, "portail-system/" + publish.ProxyBinding("vpc")}; !slices.Equal(slices.Sorted(slices.Values(proxied)), slices.Sorted(slices.Values(want))) {
		t.Errorf("staged %v, want the parameters in vpc-publish and the binding in portail's namespace", proxied)
	}
}

// A project whose publish namespace the cluster lacks is refused with the
// accesses staging would ask, and nothing reached; so are a project without
// a namespace and an install without publishing. Where the admin role writes
// HTTPRoutes already, no route grant is staged.
func TestPublishingRefusals(t *testing.T) {
	c, plat := publishingCoordinator(t, map[string][]byte{
		"rbac/web.yaml": grantYAML("web", "admin", user("alice")),
	})
	shop := project.ProjectInfo{Name: "shop", Namespaces: []string{"web"}}
	plan, need, err := c.PublishingNeeds(plat, shop, PublishTarget{})
	if !errors.Is(err, ErrNoPublishNamespace) || plan.Namespace != "shop-publish" || len(plan.Reach) != 0 || len(need) == 0 {
		t.Errorf("no publish namespace: %+v %d accesses %v, want ErrNoPublishNamespace naming shop-publish, nothing reached", plan, len(need), err)
	}
	if _, err := c.StagePublishing(auth.Identity{Username: "admin"}, plat, shop, PublishTarget{}); !errors.Is(err, ErrNoPublishNamespace) {
		t.Errorf("staged without a publish namespace: %v", err)
	}
	if _, _, err := c.PublishingNeeds(plat, project.ProjectInfo{Name: "empty"}, on(nil)); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("no namespace: %v, want ErrInvalid", err)
	}
	granted := on(nil)
	granted.RoutesGranted = true
	_, need, err = c.PublishingNeeds(plat, shop, granted)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range need {
		if a.Resource == "rolebindings" && a.Name == tenancy.RoutesBinding("web") {
			t.Errorf("admin writes routes, yet the change asks %s", a)
		}
	}
	off := newTestCoordinator(t, false)
	if _, _, err := off.PublishingNeeds(plat, shop, on(nil)); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("publishing off: %v, want ErrUnavailable", err)
	}
}

// Without an admin grant in git to follow, no Gateway read is staged: a
// provider binding its own groups there declares none here.
func TestGatewayViewFollowsAdminGrants(t *testing.T) {
	c, plat := publishingCoordinator(t, nil)
	view, err := c.StagePublishing(auth.Identity{Username: "admin"}, plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"web"}}, on(nil))
	if err != nil {
		t.Fatalf("StagePublishing: %v", err)
	}
	if _, ok := stagedYAML(t, view)[tenancy.GatewayViewBinding]; ok {
		t.Error("a Gateway read staged with no admin grant to follow")
	}
}

// Once git holds the Gateway, the certificate, the Gateway read and each
// reached namespace's route grant, nothing is left to stage; a namespace
// that joined since lacks its route grant and its admins' read, and only
// those are staged.
func TestPublishingOnceInGit(t *testing.T) {
	files, err := publish.Manifests(pubSettings, "shop", "shop-publish", false)
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
	viewPath, view, err := netgen.GatewayViewManifest(netgen.RoleBindingSpec{Namespace: "shop-publish", Kept: []netgen.Subject{user("bob")}})
	if err != nil {
		t.Fatal(err)
	}
	seed := map[string][]byte{
		"rbac/db.yaml":           grantYAML("db", "admin", user("bob")),
		"rbac/db/db-routes.yaml": routes("db", "bob"),
		"rbac/web.yaml":          grantYAML("web", "admin", user("alice")),
		viewPath:                 view,
	}
	for _, f := range files {
		seed[f.Path] = f.Content
	}
	c, plat := publishingCoordinator(t, seed)
	plan, _, err := c.PublishingNeeds(plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"db"}}, on(nil))
	if !errors.Is(err, ErrPublishingOn) || !plan.On {
		t.Errorf("all held: %+v %v, want on, ErrPublishingOn", plan, err)
	}
	staged, err := c.StagePublishing(auth.Identity{Username: "admin"}, plat, project.ProjectInfo{Name: "shop", Namespaces: []string{"db", "web"}}, on(nil))
	if err != nil {
		t.Fatalf("a joined namespace: %v", err)
	}
	items := stagedYAML(t, staged)
	if got := slices.Sorted(maps.Keys(items)); !slices.Equal(got, []string{tenancy.GatewayViewBinding, "web-routes"}) {
		t.Errorf("staged %v, want web's route grant and the Gateway read alone", got)
	}
	if _, subjects := grantSubjects(t, items[tenancy.GatewayViewBinding], tenancy.GatewayViewBinding); !slices.Equal(subjects, []netgen.Subject{user("bob"), user("alice")}) {
		t.Errorf("gateway read names %+v, want bob kept and alice added", subjects)
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

// A project the cluster holds no publish namespace for gets one staged on
// asking, labeled for it and never into it. Refused: one it holds, a project
// whose every namespace has a VM Network of its own, a namespace by that name
// held otherwise, and an install without publishing.
func TestStagePublishNamespace(t *testing.T) {
	c, plat := publishingCoordinator(t, nil)
	shop := project.ProjectInfo{Name: "shop", Namespaces: []string{"web", "db"}}
	view, err := c.StagePublishNamespace(auth.Identity{Username: "admin"}, plat, shop, PublishTarget{Networks: map[string]string{"web": "udn:web/own"}}, nil)
	if err != nil {
		t.Fatalf("StagePublishNamespace: %v", err)
	}
	items := stagedYAML(t, view)
	if y, ok := items["shop-publish"]; len(items) != 1 || !ok || !strings.Contains(y, tenancy.PublishLabel+": shop") || strings.Contains(y, "dotvirt.io/project") {
		t.Errorf("staged %v:\n%s", slices.Sorted(maps.Keys(items)), y)
	}
	if _, err := c.PublishNamespaceNeeds(plat, shop, on(nil)); !errors.Is(err, model.ErrConflict) {
		t.Errorf("held: %v, want ErrConflict", err)
	}
	own := PublishTarget{Networks: map[string]string{"web": "udn:web/own", "db": "udn:db/own"}}
	if _, err := c.PublishNamespaceNeeds(plat, shop, own); !errors.Is(err, ErrApart) {
		t.Errorf("every namespace on its own VM Network: %v, want ErrApart", err)
	}
	squatted := func(string) (*netgen.LiveNamespace, error) {
		return &netgen.LiveNamespace{Labels: map[string]string{tenancy.PublishLabel: "shop"}, Annotations: map[string]string{tenancy.RequesterAnnotation: "mallory"}}, nil
	}
	if _, err := c.StagePublishNamespace(auth.Identity{Username: "admin"}, plat, shop, PublishTarget{}, squatted); !errors.Is(err, model.ErrConflict) {
		t.Errorf("a user's own project by that name: %v, want ErrConflict", err)
	}
	if _, err := c.PublishNamespaceNeeds(plat, shop, PublishTarget{PublishNameHeld: true, PublishNameRequested: true}); !errors.Is(err, ErrPublishNameRequested) || !errors.Is(err, model.ErrConflict) {
		t.Errorf("name held by a user's own project: %v, want ErrPublishNameRequested", err)
	}
	if _, err := c.PublishNamespaceNeeds(plat, shop, PublishTarget{PublishNameHeld: true}); !errors.Is(err, ErrPublishNameHeld) || errors.Is(err, ErrPublishNameRequested) {
		t.Errorf("name held otherwise: %v, want ErrPublishNameHeld alone", err)
	}
	off := newTestCoordinator(t, false)
	if _, err := off.PublishNamespaceNeeds(plat, shop, PublishTarget{}); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("publishing off: %v, want ErrUnavailable", err)
	}
}

// A new project's VM Network never takes over one git declares, and a first
// namespace has its own VM Network or its project's, never both.
func TestProjectNetworkRefusals(t *testing.T) {
	_, declared, err := netgen.ProjectNetworkManifest("other", netgen.PrimaryNet{Name: "shared", Subnet: "10.9.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	c, plat := publishingCoordinator(t, map[string][]byte{"networks/shared.yaml": declared})
	if _, err := c.ProjectNeeds(plat, json.RawMessage(`{"name":"shop","projectNetwork":{"name":"shared","subnet":"10.40.0.0/16"}}`)); !errors.Is(err, model.ErrConflict) {
		t.Errorf("a declared network's name: %v, want ErrConflict", err)
	}
	both := `{"name":"shop","vmNetwork":{"name":"a","subnet":"10.1.0.0/24"},"projectNetwork":{"name":"b","subnet":"10.2.0.0/24"}}`
	if _, err := c.ProjectNeeds(plat, json.RawMessage(both)); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("both networks: %v, want ErrInvalid", err)
	}
	need, err := c.ProjectNeeds(plat, json.RawMessage(`{"name":"shop","projectNetwork":{"name":"shop-net","subnet":"10.40.0.0/16"}}`))
	if err != nil || !slices.ContainsFunc(need, func(a Access) bool { return a.Resource == model.KindCUDN.Plural && a.Verb == "create" }) {
		t.Errorf("project network accesses %v %v, want a ClusterUserDefinedNetwork create", need, err)
	}
}

// A project on its own VM Network gets a publish namespace on that network,
// though none of its namespaces is on the cluster's own.
func TestPublishNamespaceJoinsTheProjectNetwork(t *testing.T) {
	c, plat := publishingCoordinator(t, nil)
	vpc := project.ProjectInfo{Name: "vpc", Namespaces: []string{"x"}}
	view, err := c.StagePublishNamespace(auth.Identity{Username: "admin"}, plat, vpc, PublishTarget{ProjectNetwork: true, Networks: map[string]string{"x": "cudn:vpc-net"}}, nil)
	if err != nil {
		t.Fatalf("StagePublishNamespace: %v", err)
	}
	if y := stagedYAML(t, view)["vpc-publish"]; !strings.Contains(y, tenancy.NetworkLabel+": vpc") || !strings.Contains(y, "k8s.ovn.org/primary-user-defined-network") {
		t.Errorf("publish namespace not on the project network:\n%s", y)
	}
}
