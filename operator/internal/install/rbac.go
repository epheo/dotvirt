package install

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// The ClusterRoles these bindings reference are shipped as STATIC manifests
// (config/rbac/operand_roles.yaml), created + owned by OLM/kustomize — not authored by
// the operator. The operator only binds them per-install, which needs just `bind` on the
// named roles (no `escalate`, no ClusterRole writes). Keep the role names below in sync
// with operand_roles.yaml.

// clusterMeta is ObjectMeta for a cluster-scoped resource: no namespace, but labeled
// so the CR's finalizer can find + delete it (a namespaced CR can't ownerRef it).
func clusterMeta(name, instance string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Labels: Labels(instance)}
}

func crb(name, role, instance string, subjects []rbacv1.Subject) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"},
		ObjectMeta: clusterMeta(name, instance),
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: role},
		Subjects:   subjects,
	}
}

// DotvirtClusterRoleBinding binds the "dotvirt" read role to dotvirt's ServiceAccount.
func DotvirtClusterRoleBinding(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.ClusterRoleBinding {
	return crb("dotvirt", "dotvirt", dv.Name, []rbacv1.Subject{
		{Kind: "ServiceAccount", Name: AppName, Namespace: dv.Namespace},
	})
}

// ArgocdApplyClusterRoleBinding binds the "dotvirt-argocd-apply" role to the shared Argo
// controller SA. The AppProjects (not the role) scope which app may use cluster-scoped kinds.
func ArgocdApplyClusterRoleBinding(dv *dotvirtv1alpha1.Dotvirt, argoNS, argoSA string) *rbacv1.ClusterRoleBinding {
	return crb("dotvirt-argocd-apply", "dotvirt-argocd-apply", dv.Name, []rbacv1.Subject{
		{Kind: "ServiceAccount", Name: argoSA, Namespace: argoNS},
	})
}

// PlatformNetworkAdminBinding binds the platform-network authoring role to the
// platform-admins group (cluster-admins satisfy it implicitly).
func PlatformNetworkAdminBinding(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.ClusterRoleBinding {
	return crb("dotvirt-platform-network-admin", "dotvirt-platform-network-admin", dv.Name, []rbacv1.Subject{
		{Kind: "Group", APIGroup: "rbac.authorization.k8s.io", Name: "dotvirt-platform-admins"},
	})
}
