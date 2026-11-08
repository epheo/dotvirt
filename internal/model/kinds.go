package model

import "strings"

// The managed-object vocabulary: every Kubernetes kind dotvirt's repos
// declare, the draft resource each backs, and what the Changes pane calls a
// change to it. Draft labels, API gates, watches, renderers, adoption and the
// git index all derive their view from this table, so a kind is added here and
// nowhere else. Which kinds a tenant repo may hold is pkg/tenancy's TenantKinds,
// shared with the operator's tenant AppProject; the platform AppProject's
// whitelist mirrors this table by hand across the module boundary.

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

// RepoPath is where the repo layout files an object of kind k: where the
// generators write it and adoption captures it, so adopt-then-edit lands on one
// file. Moving a path orphans every file already committed under the old one.
// "" for a kind the layout does not place.
func (k Kind) RepoPath(namespace, name string) string {
	switch k.Kind {
	case KindVM.Kind:
		return namespace + "/" + name + ".yaml"
	case KindUDN.Kind:
		return namespace + "/networks/" + name + ".yaml"
	case KindNetpol.Kind:
		return namespace + "/networkpolicies/" + name + ".yaml"
	case KindEgressFW.Kind:
		// OVN admits one per namespace, named default.
		return namespace + "/egressfirewalls/default.yaml"
	case KindRB.Kind:
		// A namespace's tenant grant, then any other grant in it by name: each
		// in its own file, so each edits and deletes alone.
		if name == namespace+"-admins" {
			return "rbac/" + namespace + ".yaml"
		}
		return "rbac/" + namespace + "/" + name + ".yaml"
	case KindNS.Kind:
		return "namespaces/" + name + ".yaml"
	case KindCUDN.Kind:
		return "networks/" + name + ".yaml"
	case KindEgressIP.Kind:
		return "egressips/" + name + ".yaml"
	case KindExtRoute.Kind:
		return "externalroutes/" + name + ".yaml"
	case KindANP.Kind:
		return "adminnetworkpolicies/" + name + ".yaml"
	case KindBANP.Kind:
		// The cluster admits one, named default.
		return "baselineadminnetworkpolicies/default.yaml"
	case KindNNCP.Kind:
		// Filed under the uplink it renders, not its policy name
		// (netgen.UplinkPolicyName).
		return "uplinks/" + strings.TrimPrefix(name, "uplink-") + ".yaml"
	}
	return ""
}

// Resource is one draft resource: the word a draft entry, an object route and
// the SPA name a managed object by, and the kinds that back it.
type Resource struct {
	Name string
	// Kinds a manifest of this resource declares. A network is one of three
	// backings, rebalancing a file set of several kinds, the rest a single kind.
	Kinds []Kind
	// CreateLabel and EditLabel name a create and an in-place rewrite of the
	// resource in the Changes pane; Noun names it in a message. Name is
	// persisted in drafts and never shown.
	CreateLabel string
	EditLabel   string
	Noun        string
	// NetworkFamily marks the resources one form renders and reads back: the
	// object routes address them by draft identity and adoption captures them.
	// The others (VM, namespace, role binding, template, rebalancing) have routes of
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

// KindDV is a VM's disk, declared beside it in a tenant repo. It has no row:
// adoption stages one under the VM resource.
var KindDV = Kind{Kind: "DataVolume", Group: "cdi.kubevirt.io", Version: "v1beta1", Plural: "datavolumes"}

// resources is the table; names are the draft's on-disk vocabulary.
var resources = []Resource{
	{Name: "vm", Kinds: []Kind{KindVM}, CreateLabel: "Adopt VM from cluster", EditLabel: "Edit VM", Noun: "a VM"},
	{Name: "network", Kinds: []Kind{KindUDN, KindCUDN, KindNAD}, CreateLabel: "Create network", EditLabel: "Edit network", Noun: "a network", NetworkFamily: true},
	{Name: "uplink", Kinds: []Kind{KindNNCP}, CreateLabel: "Create uplink", EditLabel: "Edit uplink", Noun: "an uplink", NetworkFamily: true},
	{Name: "namespace", Kinds: []Kind{KindNS}, CreateLabel: "Create namespace", EditLabel: "Edit namespace", Noun: "a namespace"},
	{Name: "rolebinding", Kinds: []Kind{KindRB}, CreateLabel: "Grant tenant access", EditLabel: "Edit tenant access", Noun: "tenant access"},
	{Name: "egressfirewall", Kinds: []Kind{KindEgressFW}, CreateLabel: "Create outbound rules", EditLabel: "Edit outbound rules", Noun: "outbound rules", NetworkFamily: true},
	{Name: "egressip", Kinds: []Kind{KindEgressIP}, CreateLabel: "Create fixed outbound IP", EditLabel: "Edit fixed outbound IP", Noun: "a fixed outbound IP", NetworkFamily: true},
	{Name: "externalroute", Kinds: []Kind{KindExtRoute}, CreateLabel: "Create external route", EditLabel: "Edit external route", Noun: "an external route", NetworkFamily: true},
	{Name: "networkpolicy", Kinds: []Kind{KindNetpol}, CreateLabel: "Create project rule", EditLabel: "Edit project rule", Noun: "a project rule", NetworkFamily: true},
	{Name: "adminnetworkpolicy", Kinds: []Kind{KindANP}, CreateLabel: "Create cluster rule", EditLabel: "Edit cluster rule", Noun: "a cluster rule", NetworkFamily: true},
	{Name: "baselineadminnetworkpolicy", Kinds: []Kind{KindBANP}, CreateLabel: "Create baseline cluster rule", EditLabel: "Edit baseline cluster rule", Noun: "the baseline cluster rule", NetworkFamily: true},
	// The rebalancing file set: the descheduler operator install, its CR and the PSI
	// kernel-arg MachineConfig. Its Namespace document is the namespace resource.
	{Name: "drs", Kinds: []Kind{KindOpGroup, KindSub, KindDesched, KindMachCfg}, CreateLabel: "Configure rebalancing", EditLabel: "Edit rebalancing", Noun: "rebalancing"},
	{Name: "template", Kinds: []Kind{KindTemplate}, CreateLabel: "Save as template", EditLabel: "Edit template", Noun: "a template"},
}

// ResourceNoun names a draft resource in a message: the persisted name is
// not a word a user reads. An unknown name is "this kind".
func ResourceNoun(name string) string {
	if r, ok := LookupResource(name); ok && r.Noun != "" {
		return r.Noun
	}
	return "this kind"
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
