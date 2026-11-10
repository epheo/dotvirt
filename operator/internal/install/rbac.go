package install

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// The ClusterRoles these bindings reference are shipped as STATIC manifests
// (config/rbac/operand_roles.yaml), created + owned by OLM/kustomize - not authored by
// the operator. The operator only binds them per-install, which needs just `bind` on the
// named roles (no `escalate`, no ClusterRole writes). Keep the role names below in sync
// with operand_roles.yaml.

// clusterMeta is ObjectMeta for a cluster-scoped resource: no namespace, but labeled
// so the CR's finalizer can find + delete it (a namespaced CR can't ownerRef it).
func clusterMeta(name string, dv *dotvirtv1alpha1.Dotvirt) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Labels: Labels(dv)}
}

func crb(name, role string, dv *dotvirtv1alpha1.Dotvirt, subjects []rbacv1.Subject) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"},
		ObjectMeta: clusterMeta(name, dv),
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: role},
		Subjects:   subjects,
	}
}

// DotvirtClusterRoleBinding binds the "dotvirt" read role to dotvirt's ServiceAccount.
func DotvirtClusterRoleBinding(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.ClusterRoleBinding {
	return crb("dotvirt", "dotvirt", dv, []rbacv1.Subject{
		{Kind: "ServiceAccount", Name: AppName, Namespace: dv.Namespace},
	})
}

// ArgocdApplyClusterRoleBinding binds the "dotvirt-argocd-apply" role to the
// platform tier's sync account, which Argo applies the platform repo as once
// sync impersonation is on, and to the shared Argo controller SA, which
// applies every tier while it is off. The AppProjects (not the role) scope
// which app may use cluster-scoped kinds. platform=false (no platform tier)
// leaves the sync account out.
func ArgocdApplyClusterRoleBinding(dv *dotvirtv1alpha1.Dotvirt, argoNS, argoSA string, platform bool) *rbacv1.ClusterRoleBinding {
	subjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: argoSA, Namespace: argoNS}}
	if platform {
		subjects = append(subjects, rbacv1.Subject{Kind: "ServiceAccount", Name: tenancy.PlatformSyncAccount, Namespace: argoNS})
	}
	return crb("dotvirt-argocd-apply", "dotvirt-argocd-apply", dv, subjects)
}

// PlatformSyncClusterRoleBinding binds the "dotvirt-platform-sync" role to the
// platform tier's sync account: the Namespaces the platform repo declares, and
// bind on the roles its project grants name, which is what lets that account
// apply them without the controller's blanket RBAC rights.
func PlatformSyncClusterRoleBinding(dv *dotvirtv1alpha1.Dotvirt, argoNS string) *rbacv1.ClusterRoleBinding {
	return crb(tenancy.PlatformSyncAccount, tenancy.PlatformSyncAccount, dv, []rbacv1.Subject{
		{Kind: "ServiceAccount", Name: tenancy.PlatformSyncAccount, Namespace: argoNS},
	})
}

// PlatformNetworkAdminBinding binds the platform-network authoring role to the
// platform-admins group (cluster-admins satisfy it implicitly).
func PlatformNetworkAdminBinding(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.ClusterRoleBinding {
	return crb("dotvirt-platform-network-admin", "dotvirt-platform-network-admin", dv, []rbacv1.Subject{
		{Kind: "Group", APIGroup: "rbac.authorization.k8s.io", Name: "dotvirt-platform-admins"},
	})
}

// StorageCatalogBinding gives every authenticated user the storage classes
// (dotvirt-storage-catalog), as OpenShift's basic-user does; it is bound only
// where the cluster's own policy does not (no clusterversions).
func StorageCatalogBinding(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.ClusterRoleBinding {
	return crb("dotvirt-storage-catalog", "dotvirt-storage-catalog", dv, []rbacv1.Subject{
		{Kind: "Group", APIGroup: "rbac.authorization.k8s.io", Name: "system:authenticated"},
	})
}
