package changeset

import (
	"encoding/json"
	"errors"
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

// A project's grant file names its sync account beside the owners, and is
// staged even without owners: the project's repo syncs as that account. With
// no sync account configured and no owners there is nothing to grant.
func TestProjectAdoptionGrantsSyncAccount(t *testing.T) {
	specs := []netgen.NamespaceSpec{{Name: "team-a", Project: "team-a", Repo: "acme/team-a.git"}}
	grants := func(entries []draft.Entry) []draft.Entry {
		var out []draft.Entry
		for _, e := range entries {
			if e.Resource == draft.ResourceRoleBinding {
				out = append(out, e)
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
		if len(rb) != 1 || rb[0].Name != "team-a-admins" || rb[0].SourceFile != "rbac/team-a.yaml" {
			t.Fatalf("%s: grants %+v", name, rb)
		}
		_, subjects, _, err := netgen.DeclaredGrant([]byte(rb[0].Manifest), "team-a")
		if err != nil || !slices.Contains(subjects, *netgen.ServiceAccountSubject("openshift-gitops", "dotvirt-p-team-a")) {
			t.Errorf("%s: subjects %+v (%v), want the sync account", name, subjects, err)
		}
		if !strings.Contains(rb[0].Manifest, "name: dotvirt-tenant-egress") {
			t.Errorf("%s: no egress grant:\n%s", name, rb[0].Manifest)
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
// every subject; a namespace with none gets a fresh one; a file shared with
// other objects is refused rather than rewritten.
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
	files := map[string]string{}
	for _, it := range view.Items {
		files[it.Namespace] = it.YAML
	}
	sa := *netgen.ServiceAccountSubject("openshift-gitops", "dotvirt-p-legacy")
	role, subjects, _, err := netgen.DeclaredGrant([]byte(files["alpha"]), "alpha")
	if err != nil || role != "edit" || len(subjects) != 2 || subjects[0].Name != "carol" || subjects[1] != sa {
		t.Errorf("alpha grant = %q %+v (%v), want edit for carol and the sync account", role, subjects, err)
	}
	d, err := c.store.List(id.Username, plat.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range d {
		if e.Namespace == "alpha" && (e.SourceFile != "tenancy/alpha-grant.yaml" || e.Kind != draft.KindEdit) {
			t.Errorf("alpha grant written to %s (%s), want the declaring file rewritten", e.SourceFile, e.Kind)
		}
	}
	if _, subjects, _, _ := netgen.DeclaredGrant([]byte(files["beta"]), "beta"); len(subjects) != 1 || subjects[0] != sa {
		t.Errorf("beta grant subjects %+v, want the sync account alone", subjects)
	}

	if _, err := c.StageSyncAccess(id, plat, project.ProjectInfo{Name: "other", Namespaces: []string{"gamma"}}); !errors.Is(err, model.ErrConflict) {
		t.Errorf("a grant sharing its file with a Namespace: err = %v, want ErrConflict", err)
	}
	if _, err := newTestCoordinator(t, false).StageSyncAccess(id, plat, project.ProjectInfo{Name: "legacy", Namespaces: []string{"alpha"}}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("no sync namespace: err = %v, want ErrInvalid", err)
	}
}

// A namespace joining a project is staged beside its grant to that project's
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
	var grant string
	for _, it := range view.Items {
		if it.Resource == string(draft.ResourceRoleBinding) {
			grant = it.YAML
		}
	}
	_, subjects, _, err := netgen.DeclaredGrant([]byte(grant), "team-a-db")
	if err != nil || len(subjects) != 1 || subjects[0] != *netgen.ServiceAccountSubject("argocd", "dotvirt-p-team-a") {
		t.Errorf("grant subjects %+v (%v) in %d items, want the joining project's sync account", subjects, err, len(view.Items))
	}
}
