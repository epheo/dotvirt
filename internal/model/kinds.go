package model

// The managed-object vocabulary: every Kubernetes kind dotvirt's repos
// declare, the draft resource each backs, and what the Changes pane calls a
// change to it. Draft labels, API gates, watches, renderers, adoption and the
// git index all derive their view from this table, so a kind is added here and
// nowhere else. The operator's AppProject whitelists mirror it by hand: they
// live across the module boundary and ship in the same release.

// ClusterScopeNS stands in for the empty namespace of a cluster-scoped object
// wherever an identity is ns/name-shaped: draft keys and the object routes.
const ClusterScopeNS = "cluster"

// DraftNamespace is the namespace an object stages under: its own, or
// ClusterScopeNS when it has none.
func DraftNamespace(ns string) string {
	if ns == "" {
		return ClusterScopeNS
	}
	return ns
}

// ObjectNamespace is the namespace an object carries in git and on the
// cluster: none behind ClusterScopeNS.
func ObjectNamespace(ns string) string {
	if ns == ClusterScopeNS {
		return ""
	}
	return ns
}

// Kind is one managed Kubernetes kind and its API coordinates.
type Kind struct {
	Kind          string
	Group         string // "" for the core group
	Version       string
	Plural        string // the resource name SSARs and dynamic clients address
	ClusterScoped bool
}

// APIVersion is the manifest header the kind is declared under.
func (k Kind) APIVersion() string {
	if k.Group == "" {
		return k.Version
	}
	return k.Group + "/" + k.Version
}

// Resource is one draft resource: the word a draft entry, an object route and
// the SPA name a managed object by, and the kinds that back it.
type Resource struct {
	Name string
	// Kinds a manifest of this resource declares. A network is one of three
	// backings, DRS a file set of several kinds, the rest a single kind.
	Kinds []Kind
	// CreateLabel and EditLabel name a create and an in-place rewrite of the
	// resource in the Changes pane.
	CreateLabel string
	EditLabel   string
	// NetworkFamily marks the resources one form renders and reads back: the
	// object routes address them by draft identity and adoption captures them.
	// The others (VM, namespace, role binding, template, DRS) have routes of
	// their own.
	NetworkFamily bool
}

// ClusterKind is the resource's cluster-scoped backing, when it has one.
func (r Resource) ClusterKind() (Kind, bool) {
	for _, k := range r.Kinds {
		if k.ClusterScoped {
			return k, true
		}
	}
	return Kind{}, false
}

// Namespaced reports whether a backing of the resource carries a namespace.
func (r Resource) Namespaced() bool {
	for _, k := range r.Kinds {
		if !k.ClusterScoped {
			return true
		}
	}
	return false
}

// KindNames lists the resource's backings by kind.
func (r Resource) KindNames() []string {
	out := make([]string, 0, len(r.Kinds))
	for _, k := range r.Kinds {
		out = append(out, k.Kind)
	}
	return out
}

// The managed kinds, each spelled once; a consumer that knows which kind it
// wants names the var rather than looking the name up.
var (
	KindVM       = Kind{Kind: "VirtualMachine", Group: "kubevirt.io", Version: "v1", Plural: "virtualmachines"}
	KindUDN      = Kind{Kind: "UserDefinedNetwork", Group: "k8s.ovn.org", Version: "v1", Plural: "userdefinednetworks"}
	KindCUDN     = Kind{Kind: "ClusterUserDefinedNetwork", Group: "k8s.ovn.org", Version: "v1", Plural: "clusteruserdefinednetworks", ClusterScoped: true}
	KindNAD      = Kind{Kind: "NetworkAttachmentDefinition", Group: "k8s.cni.cncf.io", Version: "v1", Plural: "network-attachment-definitions"}
	KindNNCP     = Kind{Kind: "NodeNetworkConfigurationPolicy", Group: "nmstate.io", Version: "v1", Plural: "nodenetworkconfigurationpolicies", ClusterScoped: true}
	KindNS       = Kind{Kind: "Namespace", Version: "v1", Plural: "namespaces", ClusterScoped: true}
	KindRB       = Kind{Kind: "RoleBinding", Group: "rbac.authorization.k8s.io", Version: "v1", Plural: "rolebindings"}
	KindEgressFW = Kind{Kind: "EgressFirewall", Group: "k8s.ovn.org", Version: "v1", Plural: "egressfirewalls"}
	KindEgressIP = Kind{Kind: "EgressIP", Group: "k8s.ovn.org", Version: "v1", Plural: "egressips", ClusterScoped: true}
	KindExtRoute = Kind{Kind: "AdminPolicyBasedExternalRoute", Group: "k8s.ovn.org", Version: "v1", Plural: "adminpolicybasedexternalroutes", ClusterScoped: true}
	KindNetpol   = Kind{Kind: "NetworkPolicy", Group: "networking.k8s.io", Version: "v1", Plural: "networkpolicies"}
	KindANP      = Kind{Kind: "AdminNetworkPolicy", Group: "policy.networking.k8s.io", Version: "v1alpha1", Plural: "adminnetworkpolicies", ClusterScoped: true}
	KindBANP     = Kind{Kind: "BaselineAdminNetworkPolicy", Group: "policy.networking.k8s.io", Version: "v1alpha1", Plural: "baselineadminnetworkpolicies", ClusterScoped: true}
	KindOpGroup  = Kind{Kind: "OperatorGroup", Group: "operators.coreos.com", Version: "v1", Plural: "operatorgroups"}
	KindSub      = Kind{Kind: "Subscription", Group: "operators.coreos.com", Version: "v1alpha1", Plural: "subscriptions"}
	KindDesched  = Kind{Kind: "KubeDescheduler", Group: "operator.openshift.io", Version: "v1", Plural: "kubedeschedulers"}
	KindMachCfg  = Kind{Kind: "MachineConfig", Group: "machineconfiguration.openshift.io", Version: "v1", Plural: "machineconfigs", ClusterScoped: true}
	KindTemplate = Kind{Kind: "VirtualMachineTemplate", Group: "template.kubevirt.io", Version: "v1beta1", Plural: "virtualmachinetemplates"}
)

// resources is the table; names are the draft's on-disk vocabulary.
var resources = []Resource{
	{Name: "vm", Kinds: []Kind{KindVM}, CreateLabel: "Adopt VM from cluster", EditLabel: "Edit VM"},
	{Name: "network", Kinds: []Kind{KindUDN, KindCUDN, KindNAD}, CreateLabel: "Create network", EditLabel: "Edit network", NetworkFamily: true},
	{Name: "uplink", Kinds: []Kind{KindNNCP}, CreateLabel: "Create uplink", EditLabel: "Edit uplink", NetworkFamily: true},
	{Name: "namespace", Kinds: []Kind{KindNS}, CreateLabel: "Create namespace", EditLabel: "Edit namespace"},
	{Name: "rolebinding", Kinds: []Kind{KindRB}, CreateLabel: "Grant tenant access", EditLabel: "Edit tenant access"},
	{Name: "egressfirewall", Kinds: []Kind{KindEgressFW}, CreateLabel: "Create gateway firewall", EditLabel: "Edit gateway firewall", NetworkFamily: true},
	{Name: "egressip", Kinds: []Kind{KindEgressIP}, CreateLabel: "Create SNAT pool", EditLabel: "Edit SNAT pool", NetworkFamily: true},
	{Name: "externalroute", Kinds: []Kind{KindExtRoute}, CreateLabel: "Create external route", EditLabel: "Edit external route", NetworkFamily: true},
	{Name: "networkpolicy", Kinds: []Kind{KindNetpol}, CreateLabel: "Create distributed firewall policy", EditLabel: "Edit distributed firewall policy", NetworkFamily: true},
	{Name: "adminnetworkpolicy", Kinds: []Kind{KindANP}, CreateLabel: "Create admin firewall policy", EditLabel: "Edit admin firewall policy", NetworkFamily: true},
	{Name: "baselineadminnetworkpolicy", Kinds: []Kind{KindBANP}, CreateLabel: "Create baseline firewall policy", EditLabel: "Edit baseline firewall policy", NetworkFamily: true},
	// The DRS file set: the descheduler operator install, its CR and the PSI
	// kernel-arg MachineConfig. Its Namespace document is the namespace resource.
	{Name: "drs", Kinds: []Kind{KindOpGroup, KindSub, KindDesched, KindMachCfg}, CreateLabel: "Configure DRS", EditLabel: "Edit DRS"},
	{Name: "template", Kinds: []Kind{KindTemplate}, CreateLabel: "Save as template", EditLabel: "Edit template"},
}

// Resources is the whole table, read-only.
func Resources() []Resource { return resources }

// LookupResource finds a resource by draft name; "" is the VM, the draft's
// on-disk default.
func LookupResource(name string) (Resource, bool) {
	if name == "" {
		name = "vm"
	}
	for _, r := range resources {
		if r.Name == name {
			return r, true
		}
	}
	return Resource{}, false
}

// LookupKind finds the resource a kind backs.
func LookupKind(kind string) (Resource, Kind, bool) {
	for _, r := range resources {
		for _, k := range r.Kinds {
			if k.Kind == kind {
				return r, k, true
			}
		}
	}
	return Resource{}, Kind{}, false
}

// NetworkFamilyKinds lists every backing of the network family, in table order.
func NetworkFamilyKinds() []Kind {
	var out []Kind
	for _, r := range resources {
		if r.NetworkFamily {
			out = append(out, r.Kinds...)
		}
	}
	return out
}
