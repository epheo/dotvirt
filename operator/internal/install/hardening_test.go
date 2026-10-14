package install

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// The managed forge is Route-exposed: no self-registration, no anonymous read,
// and no API token in a pod that never calls the API.
func TestForgejoLockedDown(t *testing.T) {
	d := ForgejoDeployment(testDotvirt(), false, "", "")
	spec := d.Spec.Template.Spec
	for _, c := range append(spec.InitContainers, spec.Containers...) {
		for _, name := range []string{"FORGEJO__service__DISABLE_REGISTRATION", "FORGEJO__service__REQUIRE_SIGNIN_VIEW"} {
			if v, ok := envValue(c.Env, name); !ok || v != "true" {
				t.Errorf("%s: %s = (%q, %v), want true", c.Name, name, v, ok)
			}
		}
	}
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Error("Forgejo pod mounts a ServiceAccount token")
	}
}

// Two installs whose CRs share a name must not claim each other's objects; a
// pre-namespace-label object still belongs to the install of that name.
func TestOwnedBy(t *testing.T) {
	dv := testDotvirt()
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"current", Labels(dv), true},
		{"legacy name only", map[string]string{InstanceLabel: dv.Name}, true},
		{"same name other namespace", map[string]string{InstanceLabel: dv.Name, InstanceNamespaceLabel: "other"}, false},
		{"other name", map[string]string{InstanceLabel: "y", InstanceNamespaceLabel: dv.Namespace}, false},
		{"unlabelled", nil, false},
	} {
		if got := OwnedBy(tc.labels, dv); got != tc.want {
			t.Errorf("%s: OwnedBy = %v, want %v", tc.name, got, tc.want)
		}
		matched := false
		for _, s := range InstanceSelectors(dv) {
			matched = matched || s.Matches(labels.Set(tc.labels))
		}
		if matched != tc.want {
			t.Errorf("%s: InstanceSelectors match = %v, want %v (must agree with OwnedBy)", tc.name, matched, tc.want)
		}
	}
}

// The policies admit exactly the callers the install has: its own namespace
// (forge webhook delivery), ArgoCD (plugin generator, repo-server), the
// operator (forge bootstrap) and the router, on the HTTP port only.
func TestNetworkPolicyPeers(t *testing.T) {
	dv := testDotvirt()
	for _, np := range []*networkingv1.NetworkPolicy{
		OperandNetworkPolicy(dv, "openshift-gitops", "dotvirt-operator"),
		ForgejoNetworkPolicy(dv, "openshift-gitops", "dotvirt-operator"),
	} {
		if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
			t.Errorf("%s: policyTypes = %v, want Ingress only (egress stays open for webhooks and clones)", np.Name, np.Spec.PolicyTypes)
		}
		rule := np.Spec.Ingress[0]
		if len(rule.Ports) != 1 {
			t.Errorf("%s: ports = %v, want the HTTP port only", np.Name, rule.Ports)
		}
		var sameNS bool
		namespaces := map[string]bool{}
		groups := map[string]bool{}
		for _, p := range rule.From {
			if p.PodSelector != nil && p.NamespaceSelector == nil {
				sameNS = true
			}
			if p.NamespaceSelector != nil {
				for k, v := range p.NamespaceSelector.MatchLabels {
					if k == "kubernetes.io/metadata.name" {
						namespaces[v] = true
					} else {
						groups[k] = true
					}
				}
			}
		}
		if !sameNS {
			t.Errorf("%s: same-namespace peer missing", np.Name)
		}
		for _, ns := range []string{"openshift-gitops", "dotvirt-operator", "openshift-ingress"} {
			if !namespaces[ns] {
				t.Errorf("%s: namespace %s not admitted", np.Name, ns)
			}
		}
		for _, g := range []string{"policy-group.network.openshift.io/ingress", "policy-group.network.openshift.io/host-network"} {
			if !groups[g] {
				t.Errorf("%s: router group %s not admitted", np.Name, g)
			}
		}
	}
	if got := ForgejoNetworkPolicy(dv, "argocd", "").Spec.PodSelector.MatchLabels["app"]; got != ForgejoServiceName {
		t.Errorf("forge policy selects app=%q, want %q", got, ForgejoServiceName)
	}
}
