package install

import (
	"errors"
	"io"
	"maps"
	"os"
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/epheo/dotvirt/pkg/tenancy"
)

// readRoles decodes every ClusterRole in path, by name.
func readRoles(t *testing.T, path string) map[string][]rbacv1.PolicyRule {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	roles := map[string][]rbacv1.PolicyRule{}
	dec := utilyaml.NewYAMLOrJSONDecoder(f, 4096)
	for {
		role := &rbacv1.ClusterRole{}
		if err := dec.Decode(role); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		roles[role.Name] = role.Rules
	}
	return roles
}

// deskubeCommit is the DeskuBe release whose roles testdata copies: bump it
// with the copies, and dotvirt-tenant-desktops with them.
const deskubeCommit = "419a4d0"

// dotvirt-tenant-desktops holds exactly the rules of DeskuBe's vdipool editor
// and vdiclaim user roles: DeskuBe's admission asks whoever sets a pool's
// subjects for every verb the claim user role grants, so a verb missing here
// fails every such sync, and one more grants what DeskuBe never asked.
func TestDesktopsRoleIsDeskuBesPoolEditorAndClaimUser(t *testing.T) {
	verbs := func(rules []rbacv1.PolicyRule) map[string][]string {
		out := map[string][]string{}
		for _, r := range rules {
			for _, g := range r.APIGroups {
				for _, res := range r.Resources {
					out[g+"/"+res] = append(out[g+"/"+res], r.Verbs...)
				}
			}
		}
		for k := range out {
			slices.Sort(out[k])
			out[k] = slices.Compact(out[k])
		}
		return out
	}
	var source []rbacv1.PolicyRule
	for _, file := range []string{"vdipool_editor_role.yaml", "vdiclaim_user_role.yaml"} {
		for name, rules := range readRoles(t, "testdata/deskube-"+deskubeCommit+"/"+file) {
			if name != "vdipool-editor-role" && name != "vdiclaim-user-role" {
				t.Errorf("%s holds %s", file, name)
			}
			source = append(source, rules...)
		}
	}
	got := verbs(readRoles(t, "../../config/rbac/operand_roles.yaml")[tenancy.TenantDesktopsRole])
	if want := verbs(source); len(want) == 0 || !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("%s = %v, want DeskuBe %s's %v", tenancy.TenantDesktopsRole, got, deskubeCommit, want)
	}
}

// Argo gets its apply rights from static roles. A kind a repo may declare and
// no role grants passes every other check, then fails its first sync.
func TestApplyRolesCoverEveryDeclarableKind(t *testing.T) {
	roles := readRoles(t, "../../config/rbac/operand_roles.yaml")
	// The shared controller holds the first alone, so a tenant kind must be there.
	apply, sync := roles["dotvirt-argocd-apply"], roles["dotvirt-platform-sync"]
	if len(apply) == 0 || len(sync) == 0 {
		t.Fatal("the apply roles are gone from operand_roles.yaml")
	}
	grants := func(rules []rbacv1.PolicyRule, k tenancy.Kind) bool {
		return slices.ContainsFunc(rules, func(r rbacv1.PolicyRule) bool {
			return slices.Contains(r.APIGroups, k.Group) && slices.Contains(r.Resources, k.Resource) && slices.Contains(r.Verbs, "create")
		})
	}
	// An impersonated kind is applied as a project's own account alone, through
	// a role its grants bind: neither account the shared controller or the
	// platform tier applies with writes it.
	projectGrant := func(k tenancy.Kind) bool {
		return slices.ContainsFunc(tenancy.ProjectGrantRoles, func(name string) bool { return grants(roles[name], k) })
	}
	for _, k := range tenancy.Kinds {
		switch {
		case k.Tenant && !grants(apply, k):
			t.Errorf("%s/%s: tenant kind without create in dotvirt-argocd-apply", k.Group, k.Resource)
		case k.Platform && !grants(apply, k) && !grants(sync, k):
			t.Errorf("%s/%s: platform kind no apply role grants", k.Group, k.Resource)
		case k.Impersonated && (grants(apply, k) || grants(sync, k)):
			t.Errorf("%s/%s: impersonated kind the shared controller or the platform tier may write", k.Group, k.Resource)
		case k.Impersonated && !projectGrant(k):
			t.Errorf("%s/%s: impersonated kind no project grant role writes", k.Group, k.Resource)
		}
	}
	// Once impersonation is in effect the controller keeps only dotvirt-argocd-read:
	// it still diffs and health-checks every kind the apply role writes and the
	// impersonated ones, and writes none of them.
	read := roles["dotvirt-argocd-read"]
	type gr struct{ group, resource string }
	resources := func(rules []rbacv1.PolicyRule) map[gr]bool {
		out := map[gr]bool{}
		for _, r := range rules {
			for _, g := range r.APIGroups {
				for _, res := range r.Resources {
					out[gr{g, res}] = true
				}
			}
		}
		return out
	}
	want := resources(apply)
	for _, k := range tenancy.ImpersonatedKinds {
		want[gr{k.Group, k.Resource}] = true
	}
	if got := resources(read); !maps.Equal(got, want) {
		t.Errorf("dotvirt-argocd-read covers %v, want %v", got, want)
	}
	for _, r := range read {
		if !slices.Equal(r.Verbs, []string{"get", "list", "watch"}) {
			t.Errorf("dotvirt-argocd-read grants %v on %v %v, want read verbs only", r.Verbs, r.APIGroups, r.Resources)
		}
	}
	// `*` on vdipools holds seat, which lets its holder relabel a pool's
	// desktop to another claim or plant one of any image: DeskuBe's manager's
	// alone. Neither account Argo applies with may hold it.
	for _, name := range []string{"dotvirt-argocd-apply", "dotvirt-argocd-read", "dotvirt-platform-sync"} {
		for _, r := range roles[name] {
			if (slices.Contains(r.APIGroups, "vdi.desku.be") || slices.Contains(r.APIGroups, "*")) &&
				(slices.Contains(r.Verbs, "*") || slices.Contains(r.Verbs, "seat") || slices.Contains(r.Resources, "*")) {
				t.Errorf("%s grants %v on %v %v: seat is DeskuBe's manager's alone", name, r.Verbs, r.APIGroups, r.Resources)
			}
		}
	}
	// The platform repo declares no namespaced desktop kind, and Argo shows a
	// pool's desktops, pending pairing PINs included, as its children to
	// whoever reads the Application: desktops and claims stay out of every
	// role the controller or the platform tier holds.
	for _, name := range []string{"dotvirt-argocd-apply", "dotvirt-argocd-read", "dotvirt-platform-sync"} {
		for _, r := range roles[name] {
			if slices.Contains(r.APIGroups, "vdi.desku.be") && slices.ContainsFunc(r.Resources, func(res string) bool {
				return res != "vdiclasses" && res != "vdipools"
			}) {
				t.Errorf("%s grants %v on %v", name, r.Verbs, r.Resources)
			}
		}
	}
	// CDI authorizes a clone as the DataVolume's creator. The shared controller
	// holding datavolumes/source would let any tenant repo clone a disk out of
	// any namespace, so a clone is authorized only as a project's own account.
	for _, r := range apply {
		if slices.Contains(r.APIGroups, "cdi.kubevirt.io") && (slices.Contains(r.Resources, "datavolumes/source") || slices.Contains(r.Resources, "*")) {
			t.Errorf("dotvirt-argocd-apply grants %v on cdi.kubevirt.io: the shared controller could clone any namespace's disks", r.Resources)
		}
	}
}
