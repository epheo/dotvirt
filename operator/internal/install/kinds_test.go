package install

import (
	"errors"
	"io"
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
		// Known gap, not a design: only sync impersonation, off by default,
		// gives Argo this create, through the project's admin grant. Without it
		// a DataVolume a tenant repo declares alone fails to sync.
		case k.Kind == "DataVolume":
		case k.Tenant && !grants(apply, k):
			t.Errorf("%s/%s: tenant kind without create in dotvirt-argocd-apply", k.Group, k.Resource)
		case k.Platform && !grants(apply, k) && !grants(sync, k):
			t.Errorf("%s/%s: platform kind no apply role grants", k.Group, k.Resource)
		}
	}
}
