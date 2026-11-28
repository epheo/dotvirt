package tenancy

import (
	"slices"
	"testing"
)

// A provider's named kinds: each allowed once, a listed one dropped as
// already held, and grants, quotas, OLM, Argo, KubeVirt's configuration, the
// platform tier's kinds and a whole group refused.
func TestAllowTenantKinds(t *testing.T) {
	allowed, refused := AllowTenantKinds(
		GroupKind{"apps", "Deployment"},
		GroupKind{"", "Service"},
		GroupKind{"apps", "Deployment"},
		GroupKind{"kubevirt.io", "VirtualMachine"},
		GroupKind{"capsule.clastix.io", "*"},
		GroupKind{"apps", ""},
		GroupKind{"rbac.authorization.k8s.io", "Role"},
		GroupKind{"authorization.openshift.io", "RoleBinding"},
		GroupKind{"", "ResourceQuota"},
		GroupKind{"", "LimitRange"},
		GroupKind{"operators.coreos.com", "CatalogSource"},
		GroupKind{"kubevirt.io", "KubeVirt"},
		GroupKind{"hco.kubevirt.io", "HyperConverged"},
		GroupKind{"ssp.kubevirt.io", "SSP"},
		GroupKind{"argoproj.io", "Application"},
		GroupKind{"external-secrets.io", "ExternalSecret"},
		GroupKind{"forklift.konveyor.io", "Plan"},
		GroupKind{"velero.io", "Restore"},
		GroupKind{"template.openshift.io", "TemplateInstance"},
		GroupKind{"k8s.cni.cncf.io", "NetworkAttachmentDefinition"},
		GroupKind{"operator.openshift.io", "KubeDescheduler"},
		GroupKind{"template.kubevirt.io", "VirtualMachineTemplate"},
		GroupKind{"", "ResourceQuota"},
		GroupKind{"vdi.desku.be", "VdiPool"},
		GroupKind{"vdi.desku.be", "VdiClaim"},
		GroupKind{"vdi.desku.be", "VdiClass"},
		GroupKind{"vdi.desku.be", "VdiInstance"},
		GroupKind{"vdi.desku.be", "VdiPairing"},
	)
	if want := []GroupKind{{"apps", "Deployment"}, {"", "Service"}, {"vdi.desku.be", "VdiPool"}}; !slices.Equal(allowed, want) {
		t.Errorf("allowed = %v, want %v", allowed, want)
	}
	if len(refused) != 22 {
		t.Errorf("refused %d kinds, want 22 (each once): %v", len(refused), refused)
	}
	if a, r := AllowTenantKinds(); a != nil || r != nil {
		t.Errorf("none named: %v, %v", a, r)
	}
	if p := (Policy{Allowed: allowed}); !p.Extra() || (Policy{Allowed: []GroupKind{{"vdi.desku.be", "VdiPool"}}}).Extra() {
		t.Error("Extra counts the impersonated kinds as a provider's")
	}
}

// A project's repo declares its desktop pools only while Argo applies it as
// the project's own account; the platform repo the classes; no repo a desktop,
// a claim or a pairing, each a pending PIN or a person's live hold.
func TestDesktopKinds(t *testing.T) {
	for _, c := range []struct {
		kind                           string
		tenant, impersonated, platform bool
	}{
		{"VdiPool", false, true, false},
		{"VdiInstance", false, false, false},
		{"VdiClass", false, false, true},
	} {
		k, ok := LookupKind(c.kind)
		if !ok || k.Group != "vdi.desku.be" || k.Tenant != c.tenant || k.Impersonated != c.impersonated || k.Platform != c.platform || k.ClusterScoped != (c.kind == "VdiClass") {
			t.Errorf("%s = %+v, %v", c.kind, k, ok)
		}
	}
	if Listed("vdi.desku.be", "VdiPool") || !Impersonated("vdi.desku.be", "VdiPool") {
		t.Error("desktop pools are listed, or not impersonated")
	}
	for _, kind := range []string{"VdiClaim", "VdiPairing"} {
		if _, ok := LookupKind(kind); ok {
			t.Errorf("%s is in the kinds table", kind)
		}
	}
}

// No impersonated kind is also listed or cluster-scoped: a tenant AppProject
// admits no cluster-scoped kind.
func TestImpersonatedKinds(t *testing.T) {
	for _, k := range ImpersonatedKinds {
		if k.Tenant || k.ClusterScoped || k.Platform {
			t.Errorf("%s = %+v", k.Kind, k)
		}
	}
}
