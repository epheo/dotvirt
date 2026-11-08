// Package tenancy holds the rules dotvirt and its operator must apply alike to a
// tenant project's Argo wiring: the AppProject confining it, the namespaces no
// tenant may target, and the namespace its Application aims at. The plugin
// generator and the AppProjects disagreeing on any of them leaves a project
// unsynced or unconfined, hence one stdlib-only module both import, like
// pkg/forge.
package tenancy

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

// PlatformProject is the platform tier's name, reserved: a tenant by that name
// would share the platform's draft and route gates, and no tenant Application
// or AppProject is made for it.
const PlatformProject = "platform"

// AppProjectPrefix starts every tenant AppProject name. It cannot spell the
// platform tier's AppProject or the shared tenant tier's (dotvirt-platform,
// dotvirt-tenants), so no project can claim one.
const AppProjectPrefix = "dotvirt-p-"

// PlatformSyncAccount is the ServiceAccount, in the ArgoCD namespace, Argo
// impersonates to sync the platform tier once sync impersonation is on.
const PlatformSyncAccount = "dotvirt-platform-sync"

// TenantEgressRole is the ClusterRole granting EgressFirewall writes: a tenant
// kind the built-in admin role lacks, bound in each project namespace to the
// same subjects as its admin grant (EgressBinding).
const TenantEgressRole = "dotvirt-tenant-egress"

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// AppProjectName is the AppProject confining project's Application. ok=false
// for the reserved platform name and for a name that makes no valid object
// name, which the Application's own name ("dotvirt-<project>") cannot be either.
func AppProjectName(project string) (string, bool) {
	name := AppProjectPrefix + project
	if project == "" || project == PlatformProject || len(name) > 253 {
		return name, false
	}
	for _, label := range strings.Split(name, ".") {
		if !dnsLabel.MatchString(label) {
			return name, false
		}
	}
	return name, true
}

// SyncAccount is the ServiceAccount, in the ArgoCD namespace, Argo
// impersonates to sync project's Application once sync impersonation is on.
// Named like the AppProject whose destinationServiceAccounts name it, so the
// two cannot disagree. Its rights are the RoleBindings the platform repo holds
// in each project namespace (AdminBinding, EgressBinding): the project's repo
// then applies only what the project itself may.
func SyncAccount(project string) (string, bool) { return AppProjectName(project) }

// AdminBinding is the platform repo's RoleBinding granting a project namespace's
// owners, and its project's sync account, the admin role.
func AdminBinding(ns string) string { return ns + "-admins" }

// EgressBinding is the platform repo's RoleBinding granting a project
// namespace's admin-grant subjects, owners and sync account alike,
// TenantEgressRole: the project's people and its repo hold the same rights.
func EgressBinding(ns string) string { return ns + "-egress" }

// Reserved reports whether ns is platform space no tenant Application may
// target: the openshift-* and kube-* system namespaces, and platform, the
// namespaces dotvirt, its operator and ArgoCD run in.
func Reserved(ns string, platform []string) bool {
	return strings.HasPrefix(ns, "openshift-") || strings.HasPrefix(ns, "kube-") || slices.Contains(platform, ns)
}

// GroupKind is a Kubernetes kind by API group.
type GroupKind struct {
	Group string
	Kind  string
}

// TenantKinds are the only kinds a tenant repo may declare, all namespaced:
// its AppProject's whitelist and what dotvirt stages into a tenant repo. Argo
// applies them with its own rights, so a kind on this list is one a project
// may hold without doing more than its RBAC allows. A
// NetworkAttachmentDefinition is not one: a project's admin role may only read
// them, and a raw one joins another project's VLAN, reaches the underlay past
// NetworkPolicy, or takes over a NAD a CUDN generates. It is platform tier.
var TenantKinds = []GroupKind{
	{Group: "kubevirt.io", Kind: "VirtualMachine"},
	{Group: "cdi.kubevirt.io", Kind: "DataVolume"},
	{Group: "k8s.ovn.org", Kind: "UserDefinedNetwork"},
	// The Tier-1 gateway firewall: it narrows its own namespace's egress only.
	{Group: "k8s.ovn.org", Kind: "EgressFirewall"},
	// The east-west Distributed Firewall.
	{Group: "networking.k8s.io", Kind: "NetworkPolicy"},
}

// TenantKind reports whether a tenant repo may declare kind. It matches by
// kind name alone, as a manifest header names it.
func TenantKind(kind string) bool {
	return slices.ContainsFunc(TenantKinds, func(k GroupKind) bool { return k.Kind == kind })
}

// Namespace is a project namespace as Destination weighs it.
type Namespace struct {
	Name    string
	Created time.Time
}

// Destination is the namespace a project's Application targets, where Argo
// places a document without metadata.namespace: one of the project's own
// namespaces outside platform space. It must hold still as namespaces join,
// since a move re-creates those documents' objects in the new namespace and
// prunes the originals, disks included: the namespace named after the project
// wins, then the oldest, then name order. ok=false when none qualifies.
func Destination(project string, namespaces []Namespace, platform []string) (string, bool) {
	var best *Namespace
	for i := range namespaces {
		ns := &namespaces[i]
		if Reserved(ns.Name, platform) {
			continue
		}
		if ns.Name == project {
			return ns.Name, true
		}
		if best == nil || ns.Created.Before(best.Created) || (ns.Created.Equal(best.Created) && ns.Name < best.Name) {
			best = ns
		}
	}
	if best == nil {
		return "", false
	}
	return best.Name, true
}
