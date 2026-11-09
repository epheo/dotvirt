package tenancy

import "slices"

// Kind is one Kubernetes kind dotvirt's repos declare.
type Kind struct {
	Group         string // "" for the core group
	Version       string
	Kind          string
	Resource      string // the name RBAC and dynamic clients address it by
	ClusterScoped bool
	// Tenant: a project's repo may declare it. Argo applies it with its own
	// rights, so it must be one a project may hold without doing more than its
	// RBAC allows. Never widen this for one deployment's convenience: it
	// applies to every tenant.
	Tenant bool
	// Platform: the platform repo may declare it.
	Platform bool
}

// Kinds is every kind dotvirt's repos declare, spelled once. The app's
// vocabulary and the AppProject whitelists derive from it. Adoption is opt-in:
// a kind it should capture also needs a network-family row in model's resources.
// Order is the whitelists'.
var Kinds = []Kind{
	{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine", Resource: "virtualmachines", Tenant: true},
	{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataVolume", Resource: "datavolumes", Tenant: true},
	{Group: "k8s.ovn.org", Version: "v1", Kind: "UserDefinedNetwork", Resource: "userdefinednetworks", Tenant: true, Platform: true},
	// Outbound rules: they narrow their own namespace's egress only.
	{Group: "k8s.ovn.org", Version: "v1", Kind: "EgressFirewall", Resource: "egressfirewalls", Tenant: true},
	// Project rules.
	{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy", Resource: "networkpolicies", Tenant: true},

	// Not a tenant kind: a project's admin role may only read them, and a raw
	// one joins another project's VLAN, reaches the underlay past
	// NetworkPolicy, or takes over a NAD a CUDN generates.
	{Group: "k8s.cni.cncf.io", Version: "v1", Kind: "NetworkAttachmentDefinition", Resource: "network-attachment-definitions", Platform: true},
	// A project's owners: the namespace-admin grant.
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding", Resource: "rolebindings", Platform: true},
	// Rebalancing: the descheduler operator install and its configuration.
	{Group: "operators.coreos.com", Version: "v1alpha1", Kind: "Subscription", Resource: "subscriptions", Platform: true},
	{Group: "operators.coreos.com", Version: "v1", Kind: "OperatorGroup", Resource: "operatorgroups", Platform: true},
	{Group: "operator.openshift.io", Version: "v1", Kind: "KubeDescheduler", Resource: "kubedeschedulers", Platform: true},

	{Version: "v1", Kind: "Namespace", Resource: "namespaces", ClusterScoped: true, Platform: true},
	{Group: "k8s.ovn.org", Version: "v1", Kind: "ClusterUserDefinedNetwork", Resource: "clusteruserdefinednetworks", ClusterScoped: true, Platform: true},
	// The cluster edge: fixed outbound IPs and external routes.
	{Group: "k8s.ovn.org", Version: "v1", Kind: "EgressIP", Resource: "egressips", ClusterScoped: true, Platform: true},
	{Group: "k8s.ovn.org", Version: "v1", Kind: "AdminPolicyBasedExternalRoute", Resource: "adminpolicybasedexternalroutes", ClusterScoped: true, Platform: true},
	// The cluster rule tiers.
	{Group: "policy.networking.k8s.io", Version: "v1alpha1", Kind: "AdminNetworkPolicy", Resource: "adminnetworkpolicies", ClusterScoped: true, Platform: true},
	{Group: "policy.networking.k8s.io", Version: "v1alpha1", Kind: "BaselineAdminNetworkPolicy", Resource: "baselineadminnetworkpolicies", ClusterScoped: true, Platform: true},
	{Group: "nmstate.io", Version: "v1", Kind: "NodeNetworkConfigurationPolicy", Resource: "nodenetworkconfigurationpolicies", ClusterScoped: true, Platform: true},
	// Rebalancing: the worker PSI kernel argument its load-aware profile needs.
	{Group: "machineconfiguration.openshift.io", Version: "v1", Kind: "MachineConfig", Resource: "machineconfigs", ClusterScoped: true, Platform: true},

	// In a repo, never in a whitelist: Argo's source excludes templates/.
	{Group: "template.kubevirt.io", Version: "v1beta1", Kind: "VirtualMachineTemplate", Resource: "virtualmachinetemplates"},
}

// TenantKinds are the only kinds a tenant repo may declare, all namespaced:
// its AppProject's whitelist and what dotvirt stages into a tenant repo.
var TenantKinds = kindsWhere(func(k Kind) bool { return k.Tenant })

// PlatformKinds are the kinds the platform repo may declare at the given
// scope: its AppProject's whitelists.
func PlatformKinds(clusterScoped bool) []Kind {
	return kindsWhere(func(k Kind) bool { return k.Platform && k.ClusterScoped == clusterScoped })
}

func kindsWhere(keep func(Kind) bool) []Kind {
	var out []Kind
	for _, k := range Kinds {
		if keep(k) {
			out = append(out, k)
		}
	}
	return out
}

// LookupKind finds a kind by name, as a manifest header names it.
func LookupKind(kind string) (Kind, bool) {
	i := slices.IndexFunc(Kinds, func(k Kind) bool { return k.Kind == kind })
	if i < 0 {
		return Kind{}, false
	}
	return Kinds[i], true
}

// TenantKind reports whether a tenant repo may declare kind. It matches by
// kind name alone, as a manifest header names it.
func TenantKind(kind string) bool {
	k, ok := LookupKind(kind)
	return ok && k.Tenant
}
