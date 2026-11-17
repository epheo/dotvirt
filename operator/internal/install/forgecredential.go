package install

import (
	rbacv1 "k8s.io/api/rbac/v1"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// OperatorSecretNames are the Secrets the operator's own role names
// (rbac_markers.go): the only ones it may read or write without an admin's
// grant. One list with the console's (pkg/adminobjects), which decides when it
// offers the grant.
var OperatorSecretNames = adminobjects.OperatorSecrets

// OperatorServiceAccount is the operator's own ServiceAccount, as its CSV names it.
const OperatorServiceAccount = "dotvirt-operator-controller"

// ForgeCredentialGrantName names the Role and RoleBinding that let the operator
// use an admin-named forge credential.
const ForgeCredentialGrantName = adminobjects.CredentialGrantName

// credentialParams are the pins the forge credential's grant is rendered from,
// as the console takes them: the install, its credential, and the operator's
// account in operatorNS ("" unknown).
func credentialParams(dv *dotvirtv1alpha1.Dotvirt, operatorNS string) adminobjects.Params {
	p := adminobjects.Params{
		Namespace:    dv.Namespace,
		ForgeSecret:  adminobjects.ForgeSecret(dv.Spec.Forge.CredentialsSecret),
		ForgeManaged: dv.Spec.Forge.Managed,
		InstallName:  dv.Name, InstallUID: dv.UID,
	}
	if operatorNS != "" {
		p.Operator = "system:serviceaccount:" + operatorNS + ":" + OperatorServiceAccount
	}
	return p
}

// ForgeCredentialNeedsGrant reports whether spec.forge.credentialsSecret names a
// Secret the operator's own role does not cover.
func ForgeCredentialNeedsGrant(dv *dotvirtv1alpha1.Dotvirt) bool {
	return adminobjects.CredentialNeedsGrant(adminobjects.ForgeSecret(dv.Spec.Forge.CredentialsSecret))
}

// ForgeCredentialNameTaken reports whether spec.forge.credentialsSecret names
// one of the Secrets the operator generates for itself: a managed forge's token
// written there would replace that Secret's keys.
func ForgeCredentialNameTaken(dv *dotvirtv1alpha1.Dotvirt) bool {
	return adminobjects.CredentialNameTaken(adminobjects.ForgeSecret(dv.Spec.Forge.CredentialsSecret))
}

// ForgeCredentialVerbs are what the operator does with the credential: read
// it, and for a managed forge write the minted token into it.
func ForgeCredentialVerbs(dv *dotvirtv1alpha1.Dotvirt) []string {
	return adminobjects.CredentialVerbs(dv.Spec.Forge.Managed)
}

// ForgeCredentialGrant is what a cluster admin installs so the operator may use
// the Secret spec.forge.credentialsSecret names, as the console renders and
// installs it (adminobjects.CredentialGrant). The operator never applies it
// itself: granting its own account a Secret is a cluster admin's act. nil when
// no grant is needed or the operator's namespace is unknown.
func ForgeCredentialGrant(dv *dotvirtv1alpha1.Dotvirt, operatorNS string) (*rbacv1.Role, *rbacv1.RoleBinding) {
	return adminobjects.CredentialGrant(credentialParams(dv, operatorNS))
}

// ForgeCredentialCommands are the kubectl commands a cluster admin runs to
// grant the credential by hand (adminobjects.CredentialCommands).
func ForgeCredentialCommands(dv *dotvirtv1alpha1.Dotvirt, operatorNS string) []string {
	return adminobjects.CredentialCommands(credentialParams(dv, operatorNS))
}

// ForgeCredentialReplaceCommands replace a Role and RoleBinding that do not
// grant what the credential needs (adminobjects.CredentialReplaceCommands).
func ForgeCredentialReplaceCommands(dv *dotvirtv1alpha1.Dotvirt, operatorNS string) []string {
	return adminobjects.CredentialReplaceCommands(credentialParams(dv, operatorNS))
}

// OperatorAccount is the operator's ServiceAccount as namespace:name, a
// placeholder namespace when it is unknown.
func OperatorAccount(operatorNS string) string {
	return adminobjects.OperatorAccount(adminobjects.Params{Operator: credentialParams(&dotvirtv1alpha1.Dotvirt{}, operatorNS).Operator})
}
