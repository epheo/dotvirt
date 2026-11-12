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

// Argo gets its apply rights from static roles. A kind a repo may declare and
// no role grants passes every other check, then fails its first sync.
func TestApplyRolesCoverEveryDeclarableKind(t *testing.T) {
	f, err := os.Open("../../config/rbac/operand_roles.yaml")
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
			t.Fatal(err)
		}
		roles[role.Name] = role.Rules
	}
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
	for _, k := range tenancy.Kinds {
		switch {
		case k.Tenant && !grants(apply, k):
			t.Errorf("%s/%s: tenant kind without create in dotvirt-argocd-apply", k.Group, k.Resource)
		case k.Platform && !grants(apply, k) && !grants(sync, k):
			t.Errorf("%s/%s: platform kind no apply role grants", k.Group, k.Resource)
		}
	}
	// Once impersonation is in effect the controller keeps only dotvirt-argocd-read:
	// it still diffs and health-checks every kind the apply role writes, and writes
	// none of them.
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
	if got, want := resources(read), resources(apply); !maps.Equal(got, want) {
		t.Errorf("dotvirt-argocd-read covers %v, want dotvirt-argocd-apply's %v", got, want)
	}
	for _, r := range read {
		if !slices.Equal(r.Verbs, []string{"get", "list", "watch"}) {
			t.Errorf("dotvirt-argocd-read grants %v on %v %v, want read verbs only", r.Verbs, r.APIGroups, r.Resources)
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
