package controller

import (
	"context"
	"fmt"
	"slices"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/operator/internal/install"
)

// reconcileOperatorGuard keeps the admission policy that refuses the
// operator's own account any ServiceAccount token Secret: with the Secret
// rights its install needs, a token Secret is the shortest way from the
// operator's account to any other account's. Create-only: the operator holds
// create and get on the policy and its binding, never patch or delete, so a
// compromised operator cannot loosen them, and a pass that finds them changed
// says so instead of rewriting them. Best-effort: never a halt, never
// Available.
func (r *DotvirtReconciler) reconcileOperatorGuard(ctx context.Context, dv *dotvirtv1alpha1.Dotvirt, _ *reconcileCtx) (*ctrl.Result, error) {
	status, reason, msg := r.operatorGuard(ctx)
	r.setCondition(dv, dotvirtv1alpha1.ConditionOperatorGuard, status, reason, msg)
	return nil, nil
}

func (r *DotvirtReconciler) operatorGuard(ctx context.Context) (metav1.ConditionStatus, string, string) {
	if r.OperatorServiceAccount == "" {
		return metav1.ConditionUnknown, "AccountUnknown",
			"the operator does not know its own ServiceAccount (OPERATOR_SERVICE_ACCOUNT is unset, as when it runs outside its Deployment), so no policy can name it"
	}
	policy := install.OperatorGuardPolicy(r.OperatorServiceAccount)
	binding := install.OperatorGuardBinding()
	for _, want := range []client.Object{policy, binding} {
		kind := want.GetObjectKind().GroupVersionKind().Kind
		have := want.DeepCopyObject().(client.Object)
		err := r.reader().Get(ctx, client.ObjectKeyFromObject(want), have)
		switch {
		case meta.IsNoMatchError(err):
			return metav1.ConditionUnknown, "Unsupported",
				"the cluster does not serve admissionregistration.k8s.io/v1 " + kind + " (Kubernetes 1.30+), so the operator's account is not refused token Secrets"
		case apierrors.IsNotFound(err):
			// A POST create: a server-side apply would need patch, which would
			// let the operator rewrite the policy later.
			if err := r.Create(ctx, want); err != nil && !apierrors.IsAlreadyExists(err) {
				return guardFailed(kind, err)
			}
		case err != nil:
			return guardFailed(kind, err)
		default:
			if why := guardDrift(have, r.OperatorServiceAccount); why != "" {
				return metav1.ConditionFalse, "Modified", fmt.Sprintf(
					"%s %s %s; the operator cannot change it, by design: delete it and its policy or binding, and the next pass recreates both",
					kind, install.OperatorGuardName, why)
			}
		}
	}
	return metav1.ConditionTrue, "Enforced",
		"ValidatingAdmissionPolicy " + install.OperatorGuardName + " refuses ServiceAccount " + r.OperatorServiceAccount + " any token Secret"
}

func guardFailed(kind string, err error) (metav1.ConditionStatus, string, string) {
	if apierrors.IsForbidden(err) {
		return metav1.ConditionFalse, "Forbidden", "the operator may not create or read " + kind + " " + install.OperatorGuardName +
			" (" + err.Error() + "); its ClusterRole comes from the operator bundle: reinstall or upgrade the operator"
	}
	return metav1.ConditionFalse, "Failed", "could not ensure " + kind + " " + install.OperatorGuardName + ": " + err.Error()
}

// guardDrift names how a stored guard object differs from the render, ""
// when it enforces what the render does. It compares what decides who is
// refused what, not apiserver defaults.
func guardDrift(have client.Object, account string) string {
	switch h := have.(type) {
	case *admissionregistrationv1.ValidatingAdmissionPolicy:
		w := install.OperatorGuardPolicy(account)
		if got := h.Annotations[install.GuardedAccountAnnotation]; got != account {
			return fmt.Sprintf("guards ServiceAccount %q, not %q", got, account)
		}
		if h.Spec.FailurePolicy == nil || *h.Spec.FailurePolicy != *w.Spec.FailurePolicy ||
			!slices.EqualFunc(h.Spec.MatchConditions, w.Spec.MatchConditions, func(a, b admissionregistrationv1.MatchCondition) bool {
				return a.Name == b.Name && a.Expression == b.Expression
			}) ||
			!slices.EqualFunc(h.Spec.Validations, w.Spec.Validations, func(a, b admissionregistrationv1.Validation) bool {
				return a.Expression == b.Expression
			}) ||
			h.Spec.MatchConstraints == nil || !rulesEqual(h.Spec.MatchConstraints.ResourceRules, w.Spec.MatchConstraints.ResourceRules) ||
			len(h.Spec.MatchConstraints.ExcludeResourceRules) > 0 || !selectsAll(h.Spec.MatchConstraints) {
			return "was changed since the operator created it"
		}
	case *admissionregistrationv1.ValidatingAdmissionPolicyBinding:
		if h.Spec.PolicyName != install.OperatorGuardName || !slices.Contains(h.Spec.ValidationActions, admissionregistrationv1.Deny) ||
			h.Spec.ParamRef != nil || (h.Spec.MatchResources != nil && (len(h.Spec.MatchResources.ResourceRules) > 0 ||
			len(h.Spec.MatchResources.ExcludeResourceRules) > 0 || !selectsAll(h.Spec.MatchResources))) {
			return "was changed since the operator created it"
		}
	}
	return ""
}

func rulesEqual(a, b []admissionregistrationv1.NamedRuleWithOperations) bool {
	return slices.EqualFunc(a, b, func(x, y admissionregistrationv1.NamedRuleWithOperations) bool {
		return len(x.ResourceNames) == 0 && slices.Equal(x.Operations, y.Operations) &&
			slices.Equal(x.APIGroups, y.APIGroups) && slices.Equal(x.APIVersions, y.APIVersions) && slices.Equal(x.Resources, y.Resources)
	})
}

// selectsAll: the apiserver defaults both selectors to empty, which selects
// everything.
func selectsAll(m *admissionregistrationv1.MatchResources) bool {
	empty := func(s *metav1.LabelSelector) bool {
		return s == nil || (len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0)
	}
	return empty(m.NamespaceSelector) && empty(m.ObjectSelector)
}
