package install

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

var (
	readWatch = []string{"get", "list", "watch"}
	allVerbs  = []string{"get", "list", "watch", "create", "update", "patch", "delete"}
)

func rule(groups, resources, verbs []string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: groups, Resources: resources, Verbs: verbs}
}

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

// DotvirtClusterRole is the SA's background read RBAC (TokenReview, namespace/VM
// reads, Argo app patch for re-sync, wizard catalogs, network read). Read-only —
// Argo owns the apply path.
func DotvirtClusterRole(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
		ObjectMeta: clusterMeta("dotvirt", dv.Name),
		Rules: []rbacv1.PolicyRule{
			rule([]string{"authentication.k8s.io"}, []string{"tokenreviews"}, []string{"create"}),
			rule([]string{""}, []string{"namespaces"}, readWatch),
			rule([]string{"kubevirt.io"}, []string{"virtualmachines", "virtualmachineinstances"}, readWatch),
			rule([]string{"argoproj.io"}, []string{"applications"}, []string{"get", "list", "watch", "patch"}),
			rule([]string{"instancetype.kubevirt.io"}, []string{"virtualmachineclusterinstancetypes", "virtualmachineclusterpreferences"}, readWatch),
			rule([]string{"cdi.kubevirt.io"}, []string{"datasources"}, readWatch),
			rule([]string{"k8s.cni.cncf.io"}, []string{"network-attachment-definitions"}, readWatch),
			rule([]string{"storage.k8s.io"}, []string{"storageclasses"}, readWatch),
			rule([]string{"k8s.ovn.org"}, []string{"userdefinednetworks", "clusteruserdefinednetworks"}, readWatch),
			rule([]string{"nmstate.io"}, []string{"nodenetworkstates", "nodenetworkconfigurationpolicies"}, readWatch),
			rule([]string{""}, []string{"nodes"}, []string{"get", "list"}),
		},
	}
}

// DotvirtClusterRoleBinding binds the read role to dotvirt's ServiceAccount.
func DotvirtClusterRoleBinding(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.ClusterRoleBinding {
	return crb("dotvirt", "dotvirt", dv.Name, []rbacv1.Subject{
		{Kind: "ServiceAccount", Name: AppName, Namespace: dv.Namespace},
	})
}

// ArgocdApplyClusterRole grants the SHARED Argo application-controller SA the apply
// rights for the kinds dotvirt's PR flow emits. The AppProjects (not this role)
// scope which app may use the cluster-scoped kinds.
func ArgocdApplyClusterRole(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
		ObjectMeta: clusterMeta("dotvirt-argocd-apply", dv.Name),
		Rules: []rbacv1.PolicyRule{
			rule([]string{"kubevirt.io"}, []string{"virtualmachines"}, allVerbs),
			rule([]string{"k8s.ovn.org"}, []string{"userdefinednetworks", "clusteruserdefinednetworks"}, allVerbs),
			rule([]string{"k8s.cni.cncf.io"}, []string{"network-attachment-definitions"}, allVerbs),
			rule([]string{"nmstate.io"}, []string{"nodenetworkconfigurationpolicies"}, allVerbs),
			// Tenant access delegation: the platform repo's owners → namespace-admin
			// RoleBindings, scoped to the platform tier by the dotvirt-platform AppProject.
			rule([]string{"rbac.authorization.k8s.io"}, []string{"rolebindings"}, allVerbs),
		},
	}
}

// ArgocdApplyClusterRoleBinding binds the apply role to the Argo controller SA.
func ArgocdApplyClusterRoleBinding(dv *dotvirtv1alpha1.Dotvirt, argoNS, argoSA string) *rbacv1.ClusterRoleBinding {
	return crb("dotvirt-argocd-apply", "dotvirt-argocd-apply", dv.Name, []rbacv1.Subject{
		{Kind: "ServiceAccount", Name: argoSA, Namespace: argoNS},
	})
}

// PlatformNetworkAdminClusterRole is the authoring SIGNAL the UI's SSAR-gate reads
// (create cudn/nncp/namespaces). The user never creates these — Argo applies them
// from the platform repo — so it's decoupled from the apply path; bind it to
// delegate platform-network authoring to non-admins (cluster-admins satisfy it
// implicitly).
func PlatformNetworkAdminClusterRole(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
		ObjectMeta: clusterMeta("dotvirt-platform-network-admin", dv.Name),
		Rules: []rbacv1.PolicyRule{
			rule([]string{"k8s.ovn.org"}, []string{"clusteruserdefinednetworks"}, []string{"create"}),
			rule([]string{"nmstate.io"}, []string{"nodenetworkconfigurationpolicies"}, []string{"create"}),
			rule([]string{""}, []string{"namespaces"}, []string{"create"}),
		},
	}
}

// PlatformNetworkAdminBinding binds the authoring role to the platform-admins group
// (TODO: make the subjects a spec field).
func PlatformNetworkAdminBinding(dv *dotvirtv1alpha1.Dotvirt) *rbacv1.ClusterRoleBinding {
	return crb("dotvirt-platform-network-admin", "dotvirt-platform-network-admin", dv.Name, []rbacv1.Subject{
		{Kind: "Group", APIGroup: "rbac.authorization.k8s.io", Name: "dotvirt-platform-admins"},
	})
}
