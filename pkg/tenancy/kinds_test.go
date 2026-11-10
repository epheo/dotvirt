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
	)
	if want := []GroupKind{{"apps", "Deployment"}, {"", "Service"}}; !slices.Equal(allowed, want) {
		t.Errorf("allowed = %v, want %v", allowed, want)
	}
	if len(refused) != 18 {
		t.Errorf("refused %d kinds, want 18 (each once): %v", len(refused), refused)
	}
	if a, r := AllowTenantKinds(); a != nil || r != nil {
		t.Errorf("none named: %v, %v", a, r)
	}
}
