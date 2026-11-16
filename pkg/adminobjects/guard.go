package adminobjects

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

// The operator guard: admission policies bounding what the operator's own
// account may write as Secrets and Deployments. Each policy and its binding
// share a name.
const (
	GuardSecretsName     = "dotvirt-operator-secrets"
	GuardDeploymentsName = "dotvirt-operator-deployments"
)

// GuardNames are the guard's policy and binding names, in render order.
var GuardNames = []string{GuardSecretsName, GuardDeploymentsName}

// OperandAccounts are the ServiceAccounts the operator's Deployments run as:
// the console's and the managed forge's.
var OperandAccounts = []string{"dotvirt", "dotvirt-forgejo"}

// GuardParams pin the guard to one install.
type GuardParams struct {
	// Operator is the operator's exact username,
	// system:serviceaccount:<namespace>:<name>.
	Operator string
	// Namespace is the install's (the Dotvirt resource's) namespace.
	Namespace string
	// ArgoNamespace is the ArgoCD namespace the install writes into.
	ArgoNamespace string
}

// Valid reports whether every pin is known: a guard missing one would pin
// nothing.
func (p GuardParams) Valid() bool {
	return strings.HasPrefix(p.Operator, "system:serviceaccount:") && p.Namespace != "" && p.ArgoNamespace != ""
}

// Guard is the operator guard for p: its Secrets only in the install and
// ArgoCD namespaces and never a ServiceAccount token, its Deployments only in
// the install namespace, as dotvirt's own accounts, off the host. The
// apiserver evaluates it in-process, and only for Secret and Deployment
// writes; the username condition runs before any validation, so every other
// caller's write of those kinds costs one string comparison.
func Guard(p GuardParams) []Object {
	operator := `request.userInfo.username == ` + quote(p.Operator)
	policies := []*admissionregistrationv1.ValidatingAdmissionPolicy{
		policy(GuardSecretsName, "", "secrets", operator, []admissionregistrationv1.Validation{
			// Kubernetes already requires the annotation on a token-type
			// Secret, so the annotation rule refuses both today; the type
			// rule holds if that requirement ever goes.
			deny(`!has(object.type) || object.type != `+quote(string(corev1.SecretTypeServiceAccountToken)),
				"the operator never writes ServiceAccount token Secrets"),
			deny(`!has(object.metadata.annotations) || !(`+quote(corev1.ServiceAccountNameKey)+` in object.metadata.annotations)`,
				"the operator never asks for a ServiceAccount token"),
			deny(`request.namespace in [`+quote(p.Namespace)+`, `+quote(p.ArgoNamespace)+`]`,
				"the operator writes Secrets only in "+p.Namespace+" and "+p.ArgoNamespace),
		}),
		policy(GuardDeploymentsName, "apps", "deployments", operator, []admissionregistrationv1.Validation{
			deny(`request.namespace == `+quote(p.Namespace),
				"the operator runs Deployments only in "+p.Namespace),
			deny(`has(object.spec.template.spec.serviceAccountName) && object.spec.template.spec.serviceAccountName in [`+
				quote(OperandAccounts[0])+`, `+quote(OperandAccounts[1])+`]`,
				"the operator runs Deployments only as "+strings.Join(OperandAccounts, " or ")),
			deny(`!(has(object.spec.template.spec.hostNetwork) && object.spec.template.spec.hostNetwork) && `+
				`!(has(object.spec.template.spec.hostPID) && object.spec.template.spec.hostPID) && `+
				`!(has(object.spec.template.spec.hostIPC) && object.spec.template.spec.hostIPC)`,
				"the operator runs no Deployment in the host network, PID or IPC namespace"),
			deny(`!has(object.spec.template.spec.volumes) || object.spec.template.spec.volumes.all(v, !has(v.hostPath))`,
				"the operator mounts no host path"),
			deny(`object.spec.template.spec.containers.all(c, `+unprivileged+`)`,
				"the operator runs no privileged container, nor one allowed to escalate"),
			deny(`!has(object.spec.template.spec.initContainers) || object.spec.template.spec.initContainers.all(c, `+unprivileged+`)`,
				"the operator runs no privileged init container, nor one allowed to escalate"),
		}),
	}
	purpose := map[string]string{
		GuardSecretsName:     "Bounds the operator's Secrets: none outside " + p.Namespace + " and " + p.ArgoNamespace + ", never a ServiceAccount token",
		GuardDeploymentsName: "Bounds the operator's Deployments: only in " + p.Namespace + ", as dotvirt's own accounts, off the host",
	}
	var out []Object
	for _, pol := range policies {
		out = append(out, Object{Purpose: purpose[pol.Name], Object: toUnstructured(pol)})
	}
	for _, name := range GuardNames {
		out = append(out, Object{Purpose: "Enforces " + name, Object: toUnstructured(&admissionregistrationv1.ValidatingAdmissionPolicyBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicyBinding"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: guardLabels()},
			Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
				PolicyName:        name,
				ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
			},
		})})
	}
	for _, o := range out {
		stamp(o.Object)
	}
	return out
}

// unprivileged is a container that is neither privileged nor allowed to
// escalate.
const unprivileged = `!has(c.securityContext) || ` +
	`(!(has(c.securityContext.privileged) && c.securityContext.privileged) && ` +
	`!(has(c.securityContext.allowPrivilegeEscalation) && c.securityContext.allowPrivilegeEscalation))`

func policy(name, group, resource, operator string, validations []admissionregistrationv1.Validation) *admissionregistrationv1.ValidatingAdmissionPolicy {
	return &admissionregistrationv1.ValidatingAdmissionPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: guardLabels()},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: ptr.To(admissionregistrationv1.Fail),
			MatchConstraints: &admissionregistrationv1.MatchResources{
				ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{
					RuleWithOperations: admissionregistrationv1.RuleWithOperations{
						Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
						Rule:       admissionregistrationv1.Rule{APIGroups: []string{group}, APIVersions: []string{"v1"}, Resources: []string{resource}},
					},
				}},
			},
			MatchConditions: []admissionregistrationv1.MatchCondition{{Name: "operator", Expression: operator}},
			Validations:     validations,
		},
	}
}

func deny(expression, message string) admissionregistrationv1.Validation {
	return admissionregistrationv1.Validation{
		Expression: expression,
		Message:    message + " (dotvirt operator guard)",
		Reason:     ptr.To(metav1.StatusReasonForbidden),
	}
}

func guardLabels() map[string]string {
	return map[string]string{"app.kubernetes.io/name": "dotvirt", "app.kubernetes.io/part-of": "dotvirt-operator-guard"}
}

// quote is a CEL string literal; JSON's escaping is valid CEL.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func toUnstructured(o runtime.Object) *unstructured.Unstructured {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		panic(err) // the typed objects rendered here always convert
	}
	u := &unstructured.Unstructured{Object: m}
	unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, "status")
	return u
}

func samePolicy(want, live *unstructured.Unstructured) bool {
	var w, h admissionregistrationv1.ValidatingAdmissionPolicy
	if runtime.DefaultUnstructuredConverter.FromUnstructured(want.Object, &w) != nil ||
		runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, &h) != nil {
		return false
	}
	return h.Spec.FailurePolicy != nil && w.Spec.FailurePolicy != nil && *h.Spec.FailurePolicy == *w.Spec.FailurePolicy &&
		h.Spec.ParamKind == nil && len(h.Spec.Variables) == 0 &&
		slices.EqualFunc(h.Spec.MatchConditions, w.Spec.MatchConditions, func(a, b admissionregistrationv1.MatchCondition) bool {
			return a.Name == b.Name && a.Expression == b.Expression
		}) &&
		slices.EqualFunc(h.Spec.Validations, w.Spec.Validations, func(a, b admissionregistrationv1.Validation) bool {
			return a.Expression == b.Expression
		}) &&
		h.Spec.MatchConstraints != nil && w.Spec.MatchConstraints != nil && sameMatch(h.Spec.MatchConstraints, w.Spec.MatchConstraints)
}

func sameBinding(want, live *unstructured.Unstructured) bool {
	var w, h admissionregistrationv1.ValidatingAdmissionPolicyBinding
	if runtime.DefaultUnstructuredConverter.FromUnstructured(want.Object, &w) != nil ||
		runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, &h) != nil {
		return false
	}
	return h.Spec.PolicyName == w.Spec.PolicyName && slices.Contains(h.Spec.ValidationActions, admissionregistrationv1.Deny) && h.Spec.ParamRef == nil &&
		(h.Spec.MatchResources == nil || (len(h.Spec.MatchResources.ResourceRules) == 0 && len(h.Spec.MatchResources.ExcludeResourceRules) == 0 && selectsAll(h.Spec.MatchResources)))
}

func sameMatch(h, w *admissionregistrationv1.MatchResources) bool {
	return len(h.ExcludeResourceRules) == 0 && selectsAll(h) &&
		slices.EqualFunc(h.ResourceRules, w.ResourceRules, func(x, y admissionregistrationv1.NamedRuleWithOperations) bool {
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

// UsernameFromToken is the username a ServiceAccount token authenticates as:
// its JWT subject. It reads the token the pod mounts for itself, so the
// signature needs no check.
func UsernameFromToken(token string) (string, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return "", errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("JWT payload: %w", err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("JWT payload: %w", err)
	}
	if !strings.HasPrefix(claims.Sub, "system:serviceaccount:") || strings.Count(claims.Sub, ":") != 3 {
		return "", fmt.Errorf("JWT subject %q is not a ServiceAccount", claims.Sub)
	}
	return claims.Sub, nil
}
