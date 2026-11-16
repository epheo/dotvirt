package install

import (
	"slices"

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

// ForgeCredentialGrant is what a cluster admin installs so the operator may use
// the Secret spec.forge.credentialsSecret names: get on that one Secret in the
// install namespace, plus create and patch where the operator writes it (a
// managed forge's minted token), bound to the operator's ServiceAccount in
// operatorNS. The operator never applies these itself: granting its own
// account a Secret is a cluster admin's act. nil when no grant is needed or the
// operator's namespace is unknown.
func ForgeCredentialGrant(dv *dotvirtv1alpha1.Dotvirt, operatorNS string) (*rbacv1.Role, *rbacv1.RoleBinding) {
	if !ForgeCredentialNeedsGrant(dv) || operatorNS == "" {
		return nil, nil
	}
	verbs := []string{"get"}
	if dv.Spec.Forge.Managed {
		verbs = append(verbs, "create", "patch")
	}
	meta := metav1.ObjectMeta{Name: ForgeCredentialGrantName, Namespace: dv.Namespace, Labels: Labels(dv)}
	role := &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: meta,
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"secrets"},
			ResourceNames: []string{dv.Spec.Forge.CredentialsSecret}, Verbs: verbs,
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
