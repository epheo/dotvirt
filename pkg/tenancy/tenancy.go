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

// TenantEgressViewRole reads EgressFirewalls, which view and edit lack: a
// tenant kind, so without it a viewer or author loses the repo read once their
// repo declares one.
const TenantEgressViewRole = "dotvirt-tenant-egress-view"

// TenantRoutesRole is the ClusterRole granting HTTPRoute writes where the
// admin role lacks them (Gateway API ships no tenant role), bound in a
// namespace a project's Gateway reaches to the same subjects as its admin
// grant (RoutesBinding). Never aggregated: only a namespace publishing reaches
// holds it.
const TenantRoutesRole = "dotvirt-tenant-routes"

// ProjectGrantRoles are the ClusterRoles a project grant in the platform repo
// binds, the parts of the project personas: admin with TenantEgressRole,
// view or edit with TenantEgressViewRole, and TenantRoutesRole where
// publishing is on. The platform sync account may bind these and no other.
var ProjectGrantRoles = []string{"admin", TenantEgressRole, "view", "edit", TenantEgressViewRole, TenantRoutesRole}

// PathAnnotation names the folder of the repo a project lives in, beside the
// repo annotation. It only locates content: Argo and the forge grant by repo, so
// a repo stays one tenant's.
const PathAnnotation = "dotvirt.io/path"

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

// RoutesBinding is the platform repo's RoleBinding granting the admin-grant
// subjects of a namespace its project's Gateway reaches TenantRoutesRole.
func RoutesBinding(ns string) string { return ns + "-routes" }

// Reserved reports whether ns is platform space no tenant Application may
// target: the openshift-* and kube-* system namespaces, and platform, the
// namespaces dotvirt, its operator and ArgoCD run in.
func Reserved(ns string, platform []string) bool {
	return strings.HasPrefix(ns, "openshift-") || strings.HasPrefix(ns, "kube-") || slices.Contains(platform, ns)
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
