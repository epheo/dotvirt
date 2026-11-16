package install

import (
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
)

// The policy matches Secret writes only, names the operator's account before
// any validation runs, and refuses both ways to a token: the type, and the
// annotation the token controller fills.
func TestOperatorGuardPolicyRefusesTheOperatorTokenSecrets(t *testing.T) {
	p := OperatorGuardPolicy("dotvirt-operator-controller")
	if p.Name != OperatorGuardName || p.Annotations[GuardedAccountAnnotation] != "dotvirt-operator-controller" {
		t.Errorf("metadata = %v %v", p.Name, p.Annotations)
	}
	if p.Labels[InstanceLabel] != "" {
		t.Error("the guard belongs to the operator; an instance label would hand it to prune")
	}
	if p.Spec.FailurePolicy == nil || *p.Spec.FailurePolicy != admissionregistrationv1.Fail {
		t.Error("failurePolicy must be Fail")
	}
	rules := p.Spec.MatchConstraints.ResourceRules
	if len(rules) != 1 || !slices.Equal(rules[0].Resources, []string{"secrets"}) || !slices.Equal(rules[0].APIGroups, []string{""}) ||
		!slices.Equal(rules[0].Operations, []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}) {
		t.Errorf("match rules = %+v, want secrets CREATE/UPDATE only", rules)
	}
	if len(p.Spec.MatchConditions) != 1 {
		t.Fatalf("match conditions = %+v", p.Spec.MatchConditions)
	}
	want := `request.userInfo.username.startsWith("system:serviceaccount:") && request.userInfo.username.endsWith(":dotvirt-operator-controller")`
	if got := p.Spec.MatchConditions[0].Expression; got != want {
		t.Errorf("match condition = %s, want %s", got, want)
	}
	var exprs []string
	for _, v := range p.Spec.Validations {
		exprs = append(exprs, v.Expression)
		if !strings.Contains(v.Message, OperatorGuardName) {
			t.Errorf("message %q does not name the policy", v.Message)
		}
	}
	for _, needle := range []string{`object.type != "kubernetes.io/service-account-token"`, `"kubernetes.io/service-account.name" in object.metadata.annotations`} {
		if !slices.ContainsFunc(exprs, func(e string) bool { return strings.Contains(e, needle) }) {
			t.Errorf("no validation checks %s; got %q", needle, exprs)
		}
	}
	b := OperatorGuardBinding()
	if b.Name != OperatorGuardName || b.Spec.PolicyName != OperatorGuardName || b.Spec.MatchResources != nil ||
		!slices.Equal(b.Spec.ValidationActions, []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}) {
		t.Errorf("binding = %+v, want Deny on the policy, everywhere", b.Spec)
	}
}
