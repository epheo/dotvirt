package install

import (
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// OperatorGuardName names both the ValidatingAdmissionPolicy that refuses the
// operator's own account any ServiceAccount token Secret and its binding. The
// operator may create and get exactly this name, never patch or delete it, so
// a compromised operator cannot loosen the policy: a change to its content
// ships under a new name.
const OperatorGuardName = "dotvirt-operator-no-token-secrets"

// GuardedAccountAnnotation records the ServiceAccount name the policy matches,
// so a pass can tell a policy rendered for another account from its own.
const GuardedAccountAnnotation = "dotvirt.io/guarded-account"

// OperatorGuardPolicy refuses the ServiceAccount named account, in any
// namespace, a Secret that would hand it a token: the token type, or the
// annotation that asks the token controller to fill one in. The operator
// writes neither. The username check runs before any validation, so every
// other caller's Secret write costs one string comparison. Any namespace,
// because the policy outlives the install it came from: a reinstall into
// another namespace is still covered, and a same-named account elsewhere is
// only refused tokens it could not get from the operator anyway.
func OperatorGuardPolicy(account string) *admissionregistrationv1.ValidatingAdmissionPolicy {
	return &admissionregistrationv1.ValidatingAdmissionPolicy{
		TypeMeta: metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicy"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        OperatorGuardName,
			Labels:      guardLabels(),
			Annotations: map[string]string{GuardedAccountAnnotation: account},
		},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: ptr.To(admissionregistrationv1.Fail),
			MatchConstraints: &admissionregistrationv1.MatchResources{
				ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{
					RuleWithOperations: admissionregistrationv1.RuleWithOperations{
						Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{""},
							APIVersions: []string{"v1"},
							Resources:   []string{"secrets"},
						},
					},
				}},
			},
			MatchConditions: []admissionregistrationv1.MatchCondition{{
				Name: "operator-account",
				Expression: `request.userInfo.username.startsWith("system:serviceaccount:") && ` +
					`request.userInfo.username.endsWith(":` + account + `")`,
			}},
			Validations: []admissionregistrationv1.Validation{
				{
					Expression: `!has(object.type) || object.type != "` + string(corev1.SecretTypeServiceAccountToken) + `"`,
					Message:    "dotvirt's operator never writes ServiceAccount token Secrets (policy " + OperatorGuardName + ")",
					Reason:     ptr.To(metav1.StatusReasonForbidden),
				},
				{
					Expression: `!has(object.metadata.annotations) || !("` + corev1.ServiceAccountNameKey + `" in object.metadata.annotations)`,
					Message:    "dotvirt's operator never asks for a ServiceAccount token (policy " + OperatorGuardName + ")",
					Reason:     ptr.To(metav1.StatusReasonForbidden),
				},
			},
		},
	}
}

// OperatorGuardBinding enforces OperatorGuardPolicy cluster-wide.
func OperatorGuardBinding() *admissionregistrationv1.ValidatingAdmissionPolicyBinding {
	return &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicyBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: OperatorGuardName, Labels: guardLabels()},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName:        OperatorGuardName,
			ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
		},
	}
}

// guardLabels carry no instance label: the guard belongs to the operator, not
// to an install, so prune and the finalizer never consider it.
func guardLabels() map[string]string {
	return map[string]string{"app.kubernetes.io/name": AppName, "app.kubernetes.io/managed-by": fieldManager}
}
