package changeset

import (
	"encoding/json"
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
)

// A tenant named after the platform tier would share its draft and gates.
func TestTenantNameRefusesReserved(t *testing.T) {
	if err := tenantName(project.PlatformName); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("tenantName(%q) = %v, want ErrInvalid", project.PlatformName, err)
	}
	if err := tenantName("team-a"); err != nil {
		t.Fatalf("tenantName(team-a) = %v", err)
	}
	if err := tenantName("team-publish"); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("tenantName(team-publish) = %v, want ErrInvalid: its first namespace would be named like a publish namespace", err)
	}
}

// A project's grants name its sync account beside the owners, the
// EgressFirewall one in its own file to the same subjects, and are staged even
// without owners: the project's repo syncs as that account. With no sync
// account configured and no owners there is nothing to grant.
func TestProjectAdoptionGrantsSyncAccount(t *testing.T) {
	specs := []netgen.NamespaceSpec{{Name: "team-a", Project: "team-a", Repo: "acme/team-a.git"}}
	grants := func(entries []draft.Entry) map[string]draft.Entry {
		out := map[string]draft.Entry{}
		for _, e := range entries {
			if e.Resource == draft.ResourceRoleBinding {
				out[e.Name] = e
			}
		}
		return out
	}
	for name, owners := range map[string][]string{"owners": {"alice"}, "no owners": nil} {
		entries, err := projectAdoption(specs, owners, grantSource{syncNS: "openshift-gitops"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rb := grants(entries)
		admin, egress := rb["team-a-admins"], rb["team-a-egress"]
		if len(rb) != 2 || admin.SourceFile != "rbac/team-a.yaml" || egress.SourceFile != "rbac/team-a/team-a-egress.yaml" {
			t.Fatalf("%s: grants %+v", name, rb)
		}
		_, subjects, _, err := netgen.DeclaredGrant([]byte(admin.Manifest), "team-a-admins")
		if err != nil || !slices.Contains(subjects, *netgen.ServiceAccountSubject("openshift-gitops", "dotvirt-p-team-a")) || len(subjects) != len(owners)+1 {
			t.Errorf("%s: subjects %+v (%v), want the owners and the sync account", name, subjects, err)
		}
		if role, egressSubjects, _, _ := netgen.DeclaredGrant([]byte(egress.Manifest), "team-a-egress"); role != "dotvirt-tenant-egress" || !slices.Equal(egressSubjects, subjects) {
			t.Errorf("%s: egress grant %q %+v, want the admin grant's subjects", name, role, egressSubjects)
		}
	}
	entries, err := projectAdoption(specs, nil, grantSource{})
	if err != nil || len(grants(entries)) != 0 {
		t.Errorf("no owners, no sync account: grants %+v (%v), want none", grants(entries), err)
	}
}

// grantYAML is a declared admin grant of ns binding role to subjects.
func grantYAML(ns, role string, subjects ...netgen.Subject) []byte {
	_, content, err := netgen.RoleBindingManifest(netgen.RoleBindingSpec{Namespace: ns, Role: role, Kept: subjects})
	if err != nil {
		panic(err)
	}
	return content
}

func user(name string) netgen.Subject {
	return netgen.Subject{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: name}
}

func syncSA(project string) netgen.Subject {
	return *netgen.ServiceAccountSubject("openshift-gitops", "dotvirt-p-"+project)
}

func grantSubjects(t *testing.T, yaml, name string) (string, []netgen.Subject) {
	t.Helper()
	role, subjects, _, err := netgen.DeclaredGrant([]byte(yaml), name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return role, subjects
}

// The grant helper for a project made before sync impersonation rewrites the
// file declaring each namespace's grant, wherever it sits, keeping its role and
// every person; another project's sync account goes. Admins, and only admins,
// get the EgressFirewall grant to those same subjects. A file shared with
// other objects is refused rather than rewritten; a project already granted
// stages nothing.
func TestStageSyncAccessKeepsDeclaredGrant(t *testing.T) {
	bare, _ := seedWork(t, map[string][]byte{
		"tenancy/alpha-grant.yaml": grantYAML("alpha", "edit", user("carol")),
		"rbac/delta.yaml":          grantYAML("delta", "admin", user("dave"), syncSA("other")),
		"namespaces/gamma.yaml":    []byte(string(grantYAML("gamma", "admin", user("gil"))) + "---\n" + strings.ReplaceAll(declaredNS, "alpha", "gamma")),
	})
	c := newTestCoordinator(t, false).WithSyncNamespace("openshift-gitops")
	id := auth.Identity{Username: "admin"}
	plat := project.ProjectInfo{Name: "platform", Repo: bare}
	sa := syncSA("legacy")

	view, err := c.StageSyncAccess(id, plat, project.ProjectInfo{Name: "legacy", Namespaces: []string{"alpha", "beta", "delta"}})
	if err != nil {
		t.Fatalf("StageSyncAccess: %v", err)
	}
	items := map[string]string{}
	for _, it := range view.Items {
		items[it.Name] = it.YAML
	}
	if want := []string{"alpha-admins", "beta-admins", "beta-egress", "delta-admins", "delta-egress"}; !slices.Equal(slices.Sorted(maps.Keys(items)), want) {
		t.Errorf("staged %v, want %v: a narrower role gets no egress grant", slices.Sorted(maps.Keys(items)), want)
	}
	if role, subjects := grantSubjects(t, items["alpha-admins"], "alpha-admins"); role != "edit" || !slices.Equal(subjects, []netgen.Subject{user("carol"), sa}) {
		t.Errorf("alpha grant = %q %+v, want edit for carol and the sync account", role, subjects)
	}
	if _, subjects := grantSubjects(t, items["beta-admins"], "beta-admins"); !slices.Equal(subjects, []netgen.Subject{sa}) {
		t.Errorf("beta grant subjects %+v, want the sync account alone", subjects)
	}
	_, delta := grantSubjects(t, items["delta-admins"], "delta-admins")
	if !slices.Equal(delta, []netgen.Subject{user("dave"), sa}) {
		t.Errorf("delta grant subjects %+v, want dave and this project's account, not another's", delta)
	}
	// dave may stage the egress firewall the project's repo may sync.
	if _, egress := grantSubjects(t, items["delta-egress"], "delta-egress"); !slices.Equal(egress, delta) {
		t.Errorf("delta egress subjects %+v, want the admin grant's %+v", egress, delta)
	}
	d, err := c.store.List(id.Username, plat.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range d {
		if e.Name == "alpha-admins" && (e.SourceFile != "tenancy/alpha-grant.yaml" || e.Kind != draft.KindEdit) {
			t.Errorf("alpha grant written to %s (%s), want the declaring file rewritten", e.SourceFile, e.Kind)
		}
	}

	if _, err := c.StageSyncAccess(id, plat, project.ProjectInfo{Name: "other", Namespaces: []string{"gamma"}}); !errors.Is(err, model.ErrConflict) {
		t.Errorf("a grant sharing its file with a Namespace: err = %v, want ErrConflict", err)
	}
	if _, err := newTestCoordinator(t, false).StageSyncAccess(id, plat, project.ProjectInfo{Name: "legacy", Namespaces: []string{"alpha"}}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("no sync namespace: err = %v, want ErrInvalid", err)
	}

	// Merged, the grants are git's: asking again stages nothing.
	granted := map[string][]byte{}
	for _, e := range d {
		granted[e.SourceFile] = []byte(e.Manifest)
	}
	bare2, _ := seedWork(t, granted)
	_, err = newTestCoordinator(t, false).WithSyncNamespace("openshift-gitops").
		StageSyncAccess(id, project.ProjectInfo{Name: "platform", Repo: bare2}, project.ProjectInfo{Name: "legacy", Namespaces: []string{"alpha", "beta", "delta"}})
	if !errors.Is(err, model.ErrInvalid) {
		t.Errorf("already granted: err = %v, want ErrInvalid", err)
	}
}

// A namespace joining a project is granted to that project's admins, as its
// other namespaces' admin grants name them, and its sync account: the repo
// never holds there what the people do not, and nobody becomes an admin there
// through a narrower grant elsewhere. A grant the namespace already declares
// keeps its people but loses another project's sync account.
func TestStageCreateNamespaceGrantsJoiningProject(t *testing.T) {
	bare, _ := seedWork(t, map[string][]byte{
		"rbac/team-a.yaml":       grantYAML("team-a", "admin", user("alice"), syncSA("team-a")),
		"rbac/team-a-audit.yaml": grantYAML("team-a-audit", "view", user("victor"), syncSA("team-a")),
		"rbac/shared.yaml":       grantYAML("shared", "admin", user("bob"), syncSA("team-b")),
	})
	c := newTestCoordinator(t, false).WithSyncNamespace("openshift-gitops")
	plat := project.ProjectInfo{Name: project.PlatformName, Repo: bare}
	// victor only views team-a-audit: he never becomes an admin of a joining namespace.
	join := project.ProjectInfo{Name: "team-a", Repo: "acme/team-a.git", Namespaces: []string{"team-a", "team-a-audit"}}
	for ns, want := range map[string][]netgen.Subject{
		"team-a-db": {user("alice"), syncSA("team-a")},
		"shared":    {user("bob"), user("alice"), syncSA("team-a")},
	} {
		raw, _ := json.Marshal(netgen.NamespaceSpec{Name: ns})
		view, err := c.StageCreateNamespace(auth.Identity{Username: "admin"}, plat, join, raw, nil)
		if err != nil {
			t.Fatalf("%s: StageCreateNamespace: %v", ns, err)
		}
		items := map[string]string{}
		for _, it := range view.Items {
			items[it.Name] = it.YAML
		}
		if _, subjects := grantSubjects(t, items[ns+"-admins"], ns+"-admins"); !slices.Equal(subjects, want) {
			t.Errorf("%s grant subjects %+v, want %+v", ns, subjects, want)
		}
		if _, egress := grantSubjects(t, items[ns+"-egress"], ns+"-egress"); !slices.Equal(egress, want) {
			t.Errorf("%s egress subjects %+v, want %+v", ns, egress, want)
		}
	}
}

// Releasing a project takes its sync account out of its namespaces' grants:
// rewritten without it, or removed when it named nothing else, so a later
// project of that name inherits nothing.
func TestReleaseDeclaredRevokesSyncAccount(t *testing.T) {
	_, egress, err := netgen.EgressManifest(netgen.RoleBindingSpec{Namespace: "alpha", SyncAccount: ptr(syncSA("legacy"))})
	if err != nil {
		t.Fatal(err)
	}
	_, routes, err := netgen.RoutesManifest(netgen.RoleBindingSpec{Namespace: "alpha", Kept: []netgen.Subject{user("carol")}, SyncAccount: ptr(syncSA("legacy"))})
	if err != nil {
		t.Fatal(err)
	}
	bare, _ := seedWork(t, map[string][]byte{
		"rbac/alpha.yaml":              grantYAML("alpha", "admin", user("carol"), syncSA("legacy")),
		"rbac/alpha/alpha-egress.yaml": egress,
		"rbac/alpha/alpha-routes.yaml": routes,
		"rbac/beta.yaml":               grantYAML("beta", "admin", user("dan")),
		"namespaces/alpha.yaml":        []byte(declaredNS),
	})
	c := newTestCoordinator(t, false).WithSyncNamespace("openshift-gitops")
	id := auth.Identity{Username: "admin"}
	plat := project.ProjectInfo{Name: "platform", Repo: bare}
	if _, _, err := c.ReleaseDeclared(id, plat, project.ProjectInfo{Name: "legacy", Namespaces: []string{"alpha", "beta"}}); err != nil {
		t.Fatalf("ReleaseDeclared: %v", err)
	}
	d, err := c.store.List(id.Username, plat.Name)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]draft.Entry{}
	for _, e := range d {
		got[e.Name] = e
	}
	if _, ok := got["beta-admins"]; ok {
		t.Error("a grant not naming the account was rewritten")
	}
	if _, subjects := grantSubjects(t, got["alpha-admins"].Manifest, "alpha-admins"); !slices.Equal(subjects, []netgen.Subject{user("carol")}) {
		t.Errorf("alpha grant after release = %+v, want carol alone", subjects)
	}
	if e := got["alpha-egress"]; e.Kind != draft.KindDelete || e.SourceFile != "rbac/alpha/alpha-egress.yaml" {
		t.Errorf("alpha egress = %s %s, want its file removed", e.Kind, e.SourceFile)
	}
	if _, subjects := grantSubjects(t, got["alpha-routes"].Manifest, "alpha-routes"); !slices.Equal(subjects, []netgen.Subject{user("carol")}) {
		t.Errorf("alpha routes grant after release = %+v, want carol alone", subjects)
	}
}
