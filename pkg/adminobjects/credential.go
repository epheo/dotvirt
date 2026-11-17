package adminobjects

import (
	"fmt"
	"slices"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// The operator's own Secrets beside the console's: the Argo webhook secret and,
// in the ArgoCD namespace, the repo credentials and Argo's settings Secret.
const (
	ArgoWebhookSecret = "dotvirt-argo-webhook"
	RepoCredsSecret   = "dotvirt-repo-creds"
	ArgoSecret        = "argocd-secret"
)

// OperatorSecrets are the Secrets the operator's own role names
// (operator/internal/controller/rbac_markers.go): the only ones it may read or
// write without a cluster admin's grant.
var OperatorSecrets = []string{
	SessionSecret, AppsetSecret, WebhookSecret, ArgoWebhookSecret, OAuthSecret,
	ForgeAdminSecret, DefaultForgeSecret, RepoCredsSecret, ArgoSecret,
}

// CredentialGrantName names the Role and RoleBinding that let the operator use
// a forge credential an admin named.
const CredentialGrantName = "dotvirt-forge-credential"

// dotvirtAPIVersion is the install resource's API version, which owns the grant.
const dotvirtAPIVersion = "dotvirt.io/v1alpha1"

// CredentialNeedsGrant reports whether the forge credential (ForgeSecret) is a
// Secret the operator's own role does not name: a cluster admin grants it.
func CredentialNeedsGrant(forgeSecret string) bool {
	return !slices.Contains(OperatorSecrets, forgeSecret)
}

// CredentialNameTaken reports whether the forge credential is one of the
// Secrets the operator generates for itself: a managed forge's token written
// there would replace that Secret's keys, so it is refused, never granted.
func CredentialNameTaken(forgeSecret string) bool {
	return forgeSecret != DefaultForgeSecret && slices.Contains(OperatorSecrets, forgeSecret)
}

// CredentialVerbs are what the operator does with the credential: read it,
// and for a managed forge write the minted token into it.
func CredentialVerbs(managed bool) []string {
	if managed {
		return []string{"get", "create", "patch"}
	}
	return []string{"get"}
}

// CredentialGrant is what lets the operator use p.ForgeSecret: the credential's
// verbs on that one Secret in the install namespace, bound to the operator's
// account. Owned by the install (InstallName, InstallUID) without
// blockOwnerDeletion, which whoever installs it would need finalizers rights
// for, so the garbage collector takes it with the install. nil when the
// operator's own role covers the Secret or the operator is unknown.
func CredentialGrant(p Params) (*rbacv1.Role, *rbacv1.RoleBinding) {
	opNS := p.OperatorNamespace()
	if !CredentialNeedsGrant(p.ForgeSecret) || opNS == "" {
		return nil, nil
	}
	meta := metav1.ObjectMeta{Name: CredentialGrantName, Namespace: p.Namespace, Labels: credentialLabels()}
	if p.InstallName != "" && p.InstallUID != "" {
		meta.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: dotvirtAPIVersion, Kind: "Dotvirt", Name: p.InstallName, UID: p.InstallUID,
		}}
	}
	role := &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: meta,
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"secrets"},
			ResourceNames: []string{p.ForgeSecret}, Verbs: CredentialVerbs(p.ForgeManaged),
		}},
	}
	binding := &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: *meta.DeepCopy(),
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: CredentialGrantName},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: operatorAccountName(p.Operator), Namespace: opNS}},
	}
	return role, binding
}

// credentialObjects are CredentialGrant as admin objects, none when no grant is
// needed.
func credentialObjects(p Params) []Object {
	role, binding := CredentialGrant(p)
	if role == nil {
		return nil
	}
	return []Object{
		{Purpose: "Lets the operator use the forge credential " + p.ForgeSecret, Object: toUnstructured(role)},
		{Purpose: "Grants it to the operator's account", Object: toUnstructured(binding)},
	}
}

// CredentialCommands are the kubectl commands a cluster admin runs to grant
// the credential without the console: CredentialGrant without its owner, so
// it outlives the install. An unknown operator leaves a placeholder namespace.
func CredentialCommands(p Params) []string {
	return []string{
		fmt.Sprintf("kubectl -n %s create role %s --verb=%s --resource=secrets --resource-name=%s",
			p.Namespace, CredentialGrantName, strings.Join(CredentialVerbs(p.ForgeManaged), ","), p.ForgeSecret),
		fmt.Sprintf("kubectl -n %s create rolebinding %s --role=%s --serviceaccount=%s",
			p.Namespace, CredentialGrantName, CredentialGrantName, OperatorAccount(p)),
	}
}

// CredentialReplaceCommands replace a Role and RoleBinding that do not grant
// what the credential needs: delete both (a half grant included), then
// CredentialCommands.
func CredentialReplaceCommands(p Params) []string {
	return append([]string{fmt.Sprintf("kubectl -n %s delete role,rolebinding %s --ignore-not-found", p.Namespace, CredentialGrantName)},
		CredentialCommands(p)...)
}

// OperatorAccount is p.Operator as namespace:name, a placeholder namespace
// when it is unknown.
func OperatorAccount(p Params) string {
	ns := p.OperatorNamespace()
	if ns == "" {
		return "<operator-namespace>:" + operatorAccountName(p.Operator)
	}
	return ns + ":" + operatorAccountName(p.Operator)
}

// operatorAccountName is the ServiceAccount name in a username, the operator's
// default one when it does not parse.
func operatorAccountName(username string) string {
	if parts := strings.Split(username, ":"); len(parts) == 4 {
		return parts[3]
	}
	return OperatorDeployment
}

func credentialLabels() map[string]string {
	return map[string]string{"app.kubernetes.io/name": "dotvirt", "app.kubernetes.io/part-of": CredentialGrantName}
}

func diffRole(want, live *unstructured.Unstructured) string {
	var w, h rbacv1.Role
	if runtime.DefaultUnstructuredConverter.FromUnstructured(want.Object, &w) != nil ||
		runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, &h) != nil {
		return "cannot be read as a Role"
	}
	if !slices.EqualFunc(h.Rules, w.Rules, sameRule) {
		r := w.Rules[0]
		return fmt.Sprintf("does not grant exactly %s on Secret %s", strings.Join(r.Verbs, ", "), strings.Join(r.ResourceNames, ", "))
	}
	return ""
}

func diffRoleBinding(want, live *unstructured.Unstructured) string {
	var w, h rbacv1.RoleBinding
	if runtime.DefaultUnstructuredConverter.FromUnstructured(want.Object, &w) != nil ||
		runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, &h) != nil {
		return "cannot be read as a RoleBinding"
	}
	switch {
	case h.RoleRef != w.RoleRef:
		return "binds another role than " + w.RoleRef.Kind + " " + w.RoleRef.Name
	case !slices.Equal(h.Subjects, w.Subjects):
		s := w.Subjects[0]
		return "binds other accounts than " + s.Namespace + ":" + s.Name
	}
	return ""
}

// sameRule compares two rules regardless of the order of their lists.
func sameRule(a, b rbacv1.PolicyRule) bool {
	same := func(x, y []string) bool {
		return slices.Equal(slices.Sorted(slices.Values(x)), slices.Sorted(slices.Values(y)))
	}
	return same(a.APIGroups, b.APIGroups) && same(a.Resources, b.Resources) && same(a.ResourceNames, b.ResourceNames) &&
		same(a.Verbs, b.Verbs) && len(a.NonResourceURLs) == 0 && len(b.NonResourceURLs) == 0
}
