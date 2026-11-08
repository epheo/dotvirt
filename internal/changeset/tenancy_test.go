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

const declaredGrant = `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: alpha-admins
  namespace: alpha
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: edit
subjects:
- kind: User
  apiGroup: rbac.authorization.k8s.io
  name: carol
`

// The grant helper for a project made before sync impersonation rewrites the
// file declaring each namespace's grant, wherever it sits, keeping its role and
// every subject, and grants those same subjects EgressFirewall writes; a
// namespace with none gets fresh grants; a file shared with other objects is
// refused rather than rewritten; a project already granted stages nothing.
func TestStageSyncAccessKeepsDeclaredGrant(t *testing.T) {
	bare, _ := seedWork(t, map[string][]byte{
		"tenancy/alpha-grant.yaml": []byte(declaredGrant),
		"namespaces/gamma.yaml":    []byte(strings.ReplaceAll(declaredGrant, "alpha", "gamma") + "---\n" + strings.ReplaceAll(declaredNS, "alpha", "gamma")),
	})
	c := newTestCoordinator(t, false).WithSyncNamespace("openshift-gitops")
	id := auth.Identity{Username: "admin"}
	plat := project.ProjectInfo{Name: "platform", Repo: bare}

	view, err := c.StageSyncAccess(id, plat, project.ProjectInfo{Name: "legacy", Namespaces: []string{"alpha", "beta"}})
	if err != nil {
		t.Fatalf("StageSyncAccess: %v", err)
	}
	items := map[string]string{}
	for _, it := range view.Items {
		items[it.Name] = it.YAML
	}
	if len(items) != 4 || items["alpha-egress"] == "" || items["beta-egress"] == "" {
		t.Errorf("staged %v, want both namespaces' admin and egress grants", slices.Sorted(maps.Keys(items)))
	}
	sa := *netgen.ServiceAccountSubject("openshift-gitops", "dotvirt-p-legacy")
	role, subjects, _, err := netgen.DeclaredGrant([]byte(items["alpha-admins"]), "alpha-admins")
	if err != nil || role != "edit" || len(subjects) != 2 || subjects[0].Name != "carol" || subjects[1] != sa {
		t.Errorf("alpha grant = %q %+v (%v), want edit for carol and the sync account", role, subjects, err)
	}
	// carol may stage the egress firewall the project's repo may sync.
	if _, egress, _, _ := netgen.DeclaredGrant([]byte(items["alpha-egress"]), "alpha-egress"); !slices.Equal(egress, subjects) {
		t.Errorf("alpha egress subjects %+v, want the admin grant's %+v", egress, subjects)
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
	if _, subjects, _, _ := netgen.DeclaredGrant([]byte(items["beta-admins"]), "beta-admins"); len(subjects) != 1 || subjects[0] != sa {
		t.Errorf("beta grant subjects %+v, want the sync account alone", subjects)
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
		StageSyncAccess(id, project.ProjectInfo{Name: "platform", Repo: bare2}, project.ProjectInfo{Name: "legacy", Namespaces: []string{"alpha", "beta"}})
	if !errors.Is(err, model.ErrInvalid) {
		t.Errorf("already granted: err = %v, want ErrInvalid", err)
	}
}

// A namespace joining a project is staged beside its grants to that project's
// sync account, so the project syncs into every namespace it holds.
func TestStageCreateNamespaceGrantsJoiningProject(t *testing.T) {
	bare := seedBare(t)
	c := newTestCoordinator(t, false).WithSyncNamespace("argocd")
	plat := project.ProjectInfo{Name: project.PlatformName, Repo: bare}
	raw, _ := json.Marshal(netgen.NamespaceSpec{Name: "team-a-db"})
	view, err := c.StageCreateNamespace(auth.Identity{Username: "admin"}, plat, project.ProjectInfo{Name: "team-a", Repo: "acme/team-a.git"}, raw, nil)
	if err != nil {
		t.Fatalf("StageCreateNamespace: %v", err)
	}
	items := map[string]string{}
	for _, it := range view.Items {
		items[it.Name] = it.YAML
	}
	_, subjects, _, err := netgen.DeclaredGrant([]byte(items["team-a-db-admins"]), "team-a-db-admins")
	if err != nil || len(subjects) != 1 || subjects[0] != *netgen.ServiceAccountSubject("argocd", "dotvirt-p-team-a") {
		t.Errorf("grant subjects %+v (%v), want the joining project's sync account", subjects, err)
	}
	if items["team-a-db"] == "" || items["team-a-db-egress"] == "" {
		t.Errorf("staged %v, want the namespace and both grants", slices.Sorted(maps.Keys(items)))
	}
}
