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

// Argo gets its apply rights from two static roles. A kind a repo may declare
// and neither role grants passes every other check, then fails its first sync.
func TestApplyRolesCoverEveryDeclarableKind(t *testing.T) {
	f, err := os.Open("../../config/rbac/operand_roles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var rules []rbacv1.PolicyRule
	dec := utilyaml.NewYAMLOrJSONDecoder(f, 4096)
	for {
		role := &rbacv1.ClusterRole{}
		if err := dec.Decode(role); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if role.Name == "dotvirt-argocd-apply" || role.Name == "dotvirt-platform-sync" {
			rules = append(rules, role.Rules...)
		}
	}
	if len(rules) == 0 {
		t.Fatal("the apply roles are gone from operand_roles.yaml")
	}
	grants := func(k tenancy.Kind) bool {
		return slices.ContainsFunc(rules, func(r rbacv1.PolicyRule) bool {
			return slices.Contains(r.APIGroups, k.Group) && slices.Contains(r.Resources, k.Resource) && slices.Contains(r.Verbs, "create")
		})
	}
	for _, k := range tenancy.Kinds {
		// A DataVolume's create comes with the project's admin grant, through
		// CDI's aggregated role.
		if (!k.Tenant && !k.Platform) || k.Kind == "DataVolume" {
			continue
		}
		if !grants(k) {
			t.Errorf("%s/%s: no apply role grants create on it", k.Group, k.Resource)
		}
	}
}
