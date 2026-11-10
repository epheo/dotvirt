package tenancy

import (
	"slices"
	"testing"
)

// The deny list of an any-kind tenant repo: grants, quotas, OLM and KubeVirt's
// own configuration, every namespaced platform kind, and what a provider adds.
// It never denies a listed tenant kind, and holds each kind once.
func TestDeniedTenantKinds(t *testing.T) {
	denied := DeniedTenantKinds(GroupKind{"capsule.clastix.io", "*"}, GroupKind{"rbac.authorization.k8s.io", "Role"})
	covered := func(group, kind string) bool {
		return slices.ContainsFunc(denied, func(g GroupKind) bool { return g.Covers(group, kind) })
	}
	for _, c := range []struct{ group, kind string }{
		{"rbac.authorization.k8s.io", "Role"},
		{"rbac.authorization.k8s.io", "RoleBinding"},
		{"", "ResourceQuota"},
		{"", "LimitRange"},
		{"operators.coreos.com", "Subscription"},
		{"operators.coreos.com", "CatalogSource"},
		{"kubevirt.io", "KubeVirt"},
		{"k8s.cni.cncf.io", "NetworkAttachmentDefinition"},
		{"operator.openshift.io", "KubeDescheduler"},
		{"capsule.clastix.io", "Tenant"},
	} {
		if !covered(c.group, c.kind) {
			t.Errorf("%s/%s is not denied", c.group, c.kind)
		}
	}
	for _, k := range TenantKinds {
		if covered(k.Group, k.Kind) {
			t.Errorf("listed tenant kind %s is denied", k.Kind)
		}
	}
	for i, g := range denied {
		if slices.ContainsFunc(denied[:i], func(o GroupKind) bool { return o.Covers(g.Group, g.Kind) }) {
			t.Errorf("%s/%s is listed twice or under a whole-group entry", g.Group, g.Kind)
		}
	}
}
