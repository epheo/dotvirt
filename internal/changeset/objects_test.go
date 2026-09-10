package changeset

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
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

// The whole-file rule in every direction: a re-submitted namespace form that
// renames the primary network is a conflict, not an edit that prunes it; a file
// declaring the same policy name in two namespaces is never deleted whole; a
// file with documents the index cannot name is opaque; a cluster-scoped route
// is found under the cluster sentinel.
func TestSoleDeclarerRefusals(t *testing.T) {
	_, np, err := netgen.NetworkPolicyManifest(netgen.NetworkPolicySpec{Name: "default-deny", Namespace: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	_, np2, err := netgen.NetworkPolicyManifest(netgen.NetworkPolicySpec{Name: "default-deny", Namespace: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	fwPath, fw, err := netgen.EgressFirewallManifest(netgen.EgressFirewallSpec{Namespace: "alpha", Rules: []netgen.EgressRule{{Action: "Deny", CIDR: "0.0.0.0/0"}}})
	if err != nil {
		t.Fatal(err)
	}
	routePath, route, err := netgen.ExternalRouteManifest(netgen.ExternalRouteSpec{Name: "gw", Namespaces: []string{"alpha"}, NextHops: []string{"10.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	nsPath, nsContent, err := netgen.NamespaceManifest(netgen.NamespaceSpec{Name: "alpha", Project: "p", Repo: "r", VMNetwork: &netgen.PrimaryNet{Name: "vm-net", Subnet: "10.30.0.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	bare := seedBareFiles(t, map[string][]byte{
		"policies/default-deny.yaml": append(append(np, []byte("---\n")...), np2...),
		fwPath:                       append(fw, []byte("---\n# scratch notes\nfoo: bar\n")...),
		routePath:                    route,
		nsPath:                       nsContent,
	})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	if _, err := c.StageDelete(id, proj, string(draft.ResourceNetworkPolicy), "alpha", "default-deny"); !errors.Is(err, model.ErrConflict) {
		t.Errorf("same kind+name in two namespaces of one file: want ErrConflict, got %v", err)
	}
	if _, err := c.StageDelete(id, proj, string(draft.ResourceEgressFirewall), "alpha", "default"); !errors.Is(err, model.ErrConflict) {
		t.Errorf("file with an unnamed trailing document: want ErrConflict, got %v", err)
	}
	if path, err := c.locate(mustRead(t, c, proj), draft.ResourceExternalRoute, ClusterScopeNS, "gw"); err != nil || path != routePath {
		t.Errorf("cluster-scoped route: path=%q err=%v", path, err)
	}
	raw, _ := json.Marshal(netgen.NamespaceSpec{Name: "alpha", VMNetwork: &netgen.PrimaryNet{Name: "vm-net2", Subnet: "10.30.0.0/24"}})
	if _, err := c.StageCreateNamespace(id, proj, proj, raw); !errors.Is(err, model.ErrConflict) {
		t.Errorf("namespace re-submitted with another primary network: want ErrConflict, got %v", err)
	}
}

// A captured cluster-scoped object stages under the cluster sentinel with its
// platform resource, so the edit and delete paths find it by the same identity;
// one git already declares is skipped.
func TestAdoptObjectsClusterScoped(t *testing.T) {
	_, declared, err := netgen.Manifest(netgen.Spec{Name: "declared", Scope: netgen.ScopeShared, Namespaces: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	bare := seedBareFiles(t, map[string][]byte{"networks/declared.yaml": declared})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	view, err := c.AdoptObjects(id, proj, "the cluster scope", []Adoptable{
		{Kind: "ClusterUserDefinedNetwork", Name: "declared", Path: "networks/declared.yaml", Manifest: declared},
		{Kind: "AdminNetworkPolicy", Name: "iso", Path: "adminnetworkpolicies/iso.yaml", Manifest: []byte("kind: AdminNetworkPolicy\nmetadata:\n  name: iso\n")},
	})
	if err != nil {
		t.Fatalf("AdoptObjects: %v", err)
	}
	if len(view.Items) != 1 {
		t.Fatalf("want the one undeclared object staged, got %+v", view.Items)
	}
	it := view.Items[0]
	if it.Namespace != ClusterScopeNS || it.Resource != string(draft.ResourceAdminNetworkPolicy) || it.Name != "iso" {
		t.Errorf("item = %+v", it)
	}
	if _, err := c.StageDelete(id, proj, string(draft.ResourceNetwork), ClusterScopeNS, "declared"); err != nil {
		t.Errorf("the declared network must be deletable by the same identity: %v", err)
	}
}

// AdoptObject makes git say what runs: a create for an undeclared object, an
// edit for a drifted one, and a refusal when git already matches.
func TestAdoptObjectCreateOrEdit(t *testing.T) {
	path, declared, err := netgen.NetworkPolicyManifest(netgen.NetworkPolicySpec{Name: "web", Namespace: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	_, drifted, err := netgen.NetworkPolicyManifest(netgen.NetworkPolicySpec{Name: "web", Namespace: "alpha", AppliedTo: map[string]string{"app": "web"}})
	if err != nil {
		t.Fatal(err)
	}
	bare := seedBareFiles(t, map[string][]byte{path: declared})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	view, err := c.AdoptObject(id, proj, Adoptable{Kind: "NetworkPolicy", Namespace: "alpha", Name: "web", Path: path, Manifest: drifted})
	if err != nil {
		t.Fatalf("AdoptObject (drifted): %v", err)
	}
	if it := view.Items[0]; it.Kind != string(draft.KindEdit) || !strings.Contains(it.YAML, "app: web") || it.BaseYAML == "" {
		t.Errorf("drift must stage an edit against the declared file, got %+v", it)
	}
	if _, err := c.AdoptObject(id, proj, Adoptable{Kind: "NetworkPolicy", Namespace: "alpha", Name: "web", Path: path, Manifest: declared}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("a matching object must be refused, got %v", err)
	}
	view, err = c.AdoptObject(id, proj, Adoptable{Kind: "NetworkPolicy", Namespace: "alpha", Name: "api", Path: "alpha/networkpolicies/api.yaml", Manifest: []byte("kind: NetworkPolicy\nmetadata:\n  name: api\n  namespace: alpha\n")})
	if err != nil {
		t.Fatalf("AdoptObject (new): %v", err)
	}
	created := false
	for _, it := range view.Items {
		created = created || (it.Name == "api" && it.Kind == string(draft.KindCreate))
	}
	if !created {
		t.Errorf("an undeclared object must stage a create, got %+v", view.Items)
	}
}

// A manifest the form has no field for reads back with the raw manifest and a
// reason instead of a spec, and edits verbatim - as long as the replacement
// still declares the same object.
func TestObjectSpecWithoutFormAndVerbatimEdit(t *testing.T) {
	flat := "apiVersion: k8s.ovn.org/v1\nkind: ClusterUserDefinedNetwork\nmetadata:\n  name: dc-vlan\nspec:\n  namespaceSelector:\n    matchLabels:\n      dc-vlan: \"true\"\n  network:\n    localnet:\n      ipam:\n        mode: Disabled\n      physicalNetworkName: dc-vlan\n      role: Secondary\n    topology: Localnet\n"
	bare := seedBareFiles(t, map[string][]byte{"networks/dc-vlan.yaml": []byte(flat)})
	c := newTestCoordinator(t)
	id := auth.Identity{Username: "alice"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	got, err := c.ObjectSpec(proj, string(draft.ResourceNetwork), ClusterScopeNS, "dc-vlan")
	if err != nil {
		t.Fatalf("ObjectSpec: %v", err)
	}
	if got.Spec != nil || got.Reason == "" || got.Manifest != flat {
		t.Errorf("want manifest + reason and no spec, got %+v", got)
	}

	edited := strings.Replace(flat, "dc-vlan: \"true\"", "dc-vlan: \"yes\"", 1)
	view, err := c.StageUpdateManifest(id, proj, string(draft.ResourceNetwork), ClusterScopeNS, "dc-vlan", edited)
	if err != nil {
		t.Fatalf("StageUpdateManifest: %v", err)
	}
	if it := view.Items[0]; it.Kind != string(draft.KindEdit) || !strings.Contains(it.YAML, "yes") || it.BaseYAML != flat {
		t.Errorf("verbatim edit item = %+v", it)
	}
	renamed := strings.Replace(flat, "name: dc-vlan", "name: other", 1)
	if _, err := c.StageUpdateManifest(id, proj, string(draft.ResourceNetwork), ClusterScopeNS, "dc-vlan", renamed); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("a manifest declaring another object must be refused, got %v", err)
	}
	if _, err := c.StageUpdateManifest(id, proj, string(draft.ResourceNetwork), ClusterScopeNS, "dc-vlan", flat); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("an unchanged manifest must be refused, got %v", err)
	}
}

func mustRead(t *testing.T, c *Coordinator, proj project.ProjectInfo) *git.Repo {
	t.Helper()
	read, err := c.read(proj)
	if err != nil {
		t.Fatal(err)
	}
	return read
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
