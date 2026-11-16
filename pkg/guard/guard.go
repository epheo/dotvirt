// Package guard renders the admission policies that bound dotvirt's operator:
// what its own account may write as Secrets and Deployments. The operator
// holds no right to install them: a cluster admin does, from the console or
// with the command the operator reports, and the operator only reads them to
// report whether they are in place. One render serves both, so the copy an
// admin installs and the copy the operator compares are the same.
package guard

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

// The policies and their bindings share these names.
const (
	SecretsName     = "dotvirt-operator-secrets"
	DeploymentsName = "dotvirt-operator-deployments"
)

// Names are every policy and binding name, in render order.
var Names = []string{SecretsName, DeploymentsName}

// RenderAnnotation carries a digest of the render an object came from, so a
// reader tells a copy rendered for other parameters or another version
// (Outdated) from one edited by hand (Modified).
const RenderAnnotation = "dotvirt.io/guard-render"

// OperandAccounts are the ServiceAccounts the operator's Deployments run as:
// the console's and the managed forge's.
var OperandAccounts = []string{"dotvirt", "dotvirt-forgejo"}

// Params pins a render to one install.
type Params struct {
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
func (p Params) Valid() bool {
	return strings.HasPrefix(p.Operator, "system:serviceaccount:") && p.Namespace != "" && p.ArgoNamespace != ""
}

// Set is one render: a policy and a binding per name.
type Set struct {
	Policies []*admissionregistrationv1.ValidatingAdmissionPolicy
	Bindings []*admissionregistrationv1.ValidatingAdmissionPolicyBinding
}

// Render is the guard for p. The apiserver evaluates it in-process, and only
// for Secret and Deployment writes; the username condition runs before any
// validation, so every other caller's write of those kinds costs one string
// comparison.
func Render(p Params) Set {
	operator := `request.userInfo.username == ` + quote(p.Operator)
	s := Set{
		Policies: []*admissionregistrationv1.ValidatingAdmissionPolicy{
			policy(SecretsName, "", "secrets", operator, []admissionregistrationv1.Validation{
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
			policy(DeploymentsName, "apps", "deployments", operator, []admissionregistrationv1.Validation{
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
		},
	}
	for _, name := range Names {
		s.Bindings = append(s.Bindings, &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicyBinding"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels()},
			Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
				PolicyName:        name,
				ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
			},
		})
	}
	digest := s.digest()
	for _, o := range s.objects() {
		o.SetAnnotations(map[string]string{RenderAnnotation: digest})
	}
	return s
}

// unprivileged is a container that is neither privileged nor allowed to
// escalate.
const unprivileged = `!has(c.securityContext) || ` +
	`(!(has(c.securityContext.privileged) && c.securityContext.privileged) && ` +
	`!(has(c.securityContext.allowPrivilegeEscalation) && c.securityContext.allowPrivilegeEscalation))`

func policy(name, group, resource, operator string, validations []admissionregistrationv1.Validation) *admissionregistrationv1.ValidatingAdmissionPolicy {
	return &admissionregistrationv1.ValidatingAdmissionPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels()},
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

func labels() map[string]string {
	return map[string]string{"app.kubernetes.io/name": "dotvirt", "app.kubernetes.io/part-of": "dotvirt-operator-guard"}
}

// quote is a CEL string literal; JSON's escaping is valid CEL.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (s Set) objects() []metav1.Object {
	var out []metav1.Object
	for _, p := range s.Policies {
		out = append(out, p)
	}
	for _, b := range s.Bindings {
		out = append(out, b)
	}
	return out
}

func (s Set) digest() string {
	var specs []any
	for _, p := range s.Policies {
		specs = append(specs, p.Name, p.Spec)
	}
	for _, b := range s.Bindings {
		specs = append(specs, b.Name, b.Spec)
	}
	b, _ := json.Marshal(specs)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Unstructured is the set as the apply sends it.
func (s Set) Unstructured() ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured
	for _, o := range s.objects() {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			return nil, err
		}
		u := &unstructured.Unstructured{Object: m}
		unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
		unstructured.RemoveNestedField(u.Object, "status")
		out = append(out, u)
	}
	return out, nil
}

// ApplyCommand is a one-line server-side apply of the set, for an admin
// without the console.
func (s Set) ApplyCommand() (string, error) {
	objs, err := s.Unstructured()
	if err != nil {
		return "", err
	}
	items := make([]any, len(objs))
	for i, o := range objs {
		items[i] = o.Object
	}
	b, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
	if err != nil {
		return "", err
	}
	if strings.Contains(string(b), "'") {
		return "", errors.New("guard render holds a single quote")
	}
	return "echo '" + string(b) + "' | kubectl apply --server-side --field-manager=dotvirt --force-conflicts -f -", nil
}

// State is how the live guard compares with a render.
type State string

const (
	Installed    State = "Installed"
	NotInstalled State = "NotInstalled"
	// Outdated: rendered for other pins or by another version.
	Outdated State = "Outdated"
	// Modified: rendered as now, then edited.
	Modified State = "Modified"
)

// Compare weighs the live objects, by name (nil when absent), against want.
// detail names the first object that is not as rendered.
func Compare(want Set, policies map[string]*admissionregistrationv1.ValidatingAdmissionPolicy,
	bindings map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding) (state State, detail string) {
	for _, w := range want.Policies {
		h := policies[w.Name]
		if h == nil {
			return NotInstalled, "ValidatingAdmissionPolicy " + w.Name + " is missing"
		}
		if h.Annotations[RenderAnnotation] != w.Annotations[RenderAnnotation] {
			return Outdated, "ValidatingAdmissionPolicy " + w.Name + " was rendered for another install or version"
		}
		if !samePolicy(h.Spec, w.Spec) {
			return Modified, "ValidatingAdmissionPolicy " + w.Name + " was changed after it was installed"
		}
	}
	for _, w := range want.Bindings {
		h := bindings[w.Name]
		if h == nil {
			return NotInstalled, "ValidatingAdmissionPolicyBinding " + w.Name + " is missing"
		}
		if h.Annotations[RenderAnnotation] != w.Annotations[RenderAnnotation] {
			return Outdated, "ValidatingAdmissionPolicyBinding " + w.Name + " was rendered for another install or version"
		}
		if !sameBinding(h.Spec, w.Spec) {
			return Modified, "ValidatingAdmissionPolicyBinding " + w.Name + " was changed after it was installed"
		}
	}
	return Installed, ""
}

// samePolicy compares what decides who is refused what, not apiserver
// defaults.
func samePolicy(h, w admissionregistrationv1.ValidatingAdmissionPolicySpec) bool {
	return h.FailurePolicy != nil && *h.FailurePolicy == *w.FailurePolicy && h.ParamKind == nil && len(h.Variables) == 0 &&
		slices.EqualFunc(h.MatchConditions, w.MatchConditions, func(a, b admissionregistrationv1.MatchCondition) bool {
			return a.Name == b.Name && a.Expression == b.Expression
		}) &&
		slices.EqualFunc(h.Validations, w.Validations, func(a, b admissionregistrationv1.Validation) bool {
			return a.Expression == b.Expression
		}) &&
		h.MatchConstraints != nil && sameMatch(h.MatchConstraints, w.MatchConstraints)
}

func sameBinding(h, w admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec) bool {
	return h.PolicyName == w.PolicyName && slices.Contains(h.ValidationActions, admissionregistrationv1.Deny) && h.ParamRef == nil &&
		(h.MatchResources == nil || (len(h.MatchResources.ResourceRules) == 0 && len(h.MatchResources.ExcludeResourceRules) == 0 && selectsAll(h.MatchResources)))
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
