package install

import (
	"fmt"
	"slices"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
)

// OperatorSecretNames are the Secrets the operator's own role names
// (rbac_markers.go): the only ones it may read or write without an admin's grant.
var OperatorSecretNames = []string{
	SessionSecretName, AppsetSecretName, WebhookSecretName, ArgoWebhookSecretName,
	OAuthSecretName, ForgejoAdminSecret, defaultForgeSecret, RepoCredsName, ArgoSecretName,
}

// OperatorServiceAccount is the operator's own ServiceAccount, as its CSV names it.
const OperatorServiceAccount = "dotvirt-operator-controller"

// ForgeCredentialGrantName names the Role and RoleBinding that let the operator
// use an admin-named forge credential.
const ForgeCredentialGrantName = "dotvirt-forge-credential"

// ForgeCredentialNeedsGrant reports whether spec.forge.credentialsSecret names a
// Secret the operator's own role does not cover.
func ForgeCredentialNeedsGrant(dv *dotvirtv1alpha1.Dotvirt) bool {
	name := dv.Spec.Forge.CredentialsSecret
	return name != "" && !slices.Contains(OperatorSecretNames, name)
}

// ForgeCredentialNameTaken reports whether spec.forge.credentialsSecret names
// one of the Secrets the operator generates for itself: a managed forge's token
// written there would replace that Secret's keys.
func ForgeCredentialNameTaken(dv *dotvirtv1alpha1.Dotvirt) bool {
	name := dv.Spec.Forge.CredentialsSecret
	return name != defaultForgeSecret && slices.Contains(OperatorSecretNames, name)
}

// forgeCredentialVerbs are what the operator does with the credential: read
// it, and for a managed forge write the minted token into it.
func forgeCredentialVerbs(dv *dotvirtv1alpha1.Dotvirt) []string {
	if dv.Spec.Forge.Managed {
		return []string{"get", "create", "patch"}
	}
	return []string{"get"}
}

// ForgeCredentialGrant is what a cluster admin installs so the operator may use
// the Secret spec.forge.credentialsSecret names: the credential's verbs on that
// one Secret in the install namespace, bound to the operator's ServiceAccount
// in operatorNS. The operator never applies these itself: granting its own
// account a Secret is a cluster admin's act. Owned by the CR, without
// blockOwnerDeletion (which the installer would need finalizers rights for), so
// the garbage collector takes the grant with the install. nil when no grant is
// needed or the operator's namespace is unknown.
func ForgeCredentialGrant(dv *dotvirtv1alpha1.Dotvirt, operatorNS string) (*rbacv1.Role, *rbacv1.RoleBinding) {
	if !ForgeCredentialNeedsGrant(dv) || operatorNS == "" {
		return nil, nil
	}
	meta := metav1.ObjectMeta{
		Name: ForgeCredentialGrantName, Namespace: dv.Namespace, Labels: Labels(dv),
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: dotvirtv1alpha1.GroupVersion.String(), Kind: "Dotvirt", Name: dv.Name, UID: dv.UID,
		}},
	}
	role := &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: meta,
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"secrets"},
			ResourceNames: []string{dv.Spec.Forge.CredentialsSecret}, Verbs: forgeCredentialVerbs(dv),
		}},
	}
	binding := &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: *meta.DeepCopy(),
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: ForgeCredentialGrantName},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: OperatorServiceAccount, Namespace: operatorNS}},
	}
	return role, binding
}

// ForgeCredentialCommands are the kubectl commands a cluster admin runs to
// grant the credential by hand: the same Role and RoleBinding as
// ForgeCredentialGrant, without its owner, so they outlive the install.
// operatorNS "" leaves a placeholder.
func ForgeCredentialCommands(dv *dotvirtv1alpha1.Dotvirt, operatorNS string) []string {
	if operatorNS == "" {
		operatorNS = "<operator-namespace>"
	}
	return []string{
		fmt.Sprintf("kubectl -n %s create role %s --verb=%s --resource=secrets --resource-name=%s",
			dv.Namespace, ForgeCredentialGrantName, strings.Join(forgeCredentialVerbs(dv), ","), dv.Spec.Forge.CredentialsSecret),
		fmt.Sprintf("kubectl -n %s create rolebinding %s --role=%s --serviceaccount=%s:%s",
			dv.Namespace, ForgeCredentialGrantName, ForgeCredentialGrantName, operatorNS, OperatorServiceAccount),
	}
}
