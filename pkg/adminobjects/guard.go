package adminobjects

import (
	"encoding/json"
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
// account may write as Secrets, Deployments and RBAC bindings. Each policy and
// its binding share a name.
const (
	GuardSecretsName     = "dotvirt-operator-secrets"
	GuardDeploymentsName = "dotvirt-operator-deployments"
	GuardBindingsName    = "dotvirt-operator-bindings"
)

// GuardNames are the guard's policy and binding names, in render order.
var GuardNames = []string{GuardSecretsName, GuardDeploymentsName, GuardBindingsName}

// OperandAccounts are the ServiceAccounts the operator's Deployments run as:
// the console's and the managed forge's.
var OperandAccounts = []string{"dotvirt", "dotvirt-forgejo"}

// Repositories are the official image repositories: any release in them
// passes, so an upgrade needs no new guard. The operator's built-in default
// images come from these (a test in the operator holds them together).
var Repositories = []string{"quay.io/epheo/dotvirt", "codeberg.org/forgejo/forgejo"}

// PlatformSyncAccount is the platform tier's sync account in the ArgoCD
// namespace (pkg/tenancy.PlatformSyncAccount; a test holds them together).
const PlatformSyncAccount = "dotvirt-platform-sync"

// Guard is the operator guard for p:
//   - Secrets only in the install and ArgoCD namespaces, never a
//     ServiceAccount token;
//   - Deployments only in the install namespace, as dotvirt's own accounts,
//     from the official repositories or an image an admin chose, off the host
//     and unprivileged;
//   - RBAC bindings only the ones the operator renders: their names, roles and
//     subjects.
//
// The apiserver evaluates it in-process, only for those kinds' writes; the
// username condition runs first, so every other caller's write costs one
// string comparison.
func Guard(p Params) []Object {
	operator := `request.userInfo.username == ` + quote(p.Operator)
	policies := []*admissionregistrationv1.ValidatingAdmissionPolicy{
		policy(GuardSecretsName, rules("", "secrets"), operator, []admissionregistrationv1.Validation{
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
		policy(GuardDeploymentsName, rules("apps", "deployments"), operator, []admissionregistrationv1.Validation{
			deny(`request.namespace == `+quote(p.Namespace),
				"the operator runs Deployments only in "+p.Namespace),
			deny(`has(object.spec.template.spec.serviceAccountName) && object.spec.template.spec.serviceAccountName in `+list(OperandAccounts...),
				"the operator runs Deployments only as "+strings.Join(OperandAccounts, " or ")),
			deny(eachContainer(imageAllowed(p)),
				"the operator runs only images from "+strings.Join(Repositories, " or ")+", or the image an admin chose"),
			deny(`!(has(object.spec.template.spec.hostNetwork) && object.spec.template.spec.hostNetwork) && `+
				`!(has(object.spec.template.spec.hostPID) && object.spec.template.spec.hostPID) && `+
				`!(has(object.spec.template.spec.hostIPC) && object.spec.template.spec.hostIPC)`,
				"the operator runs no Deployment in the host network, PID or IPC namespace"),
			deny(`!has(object.spec.template.spec.volumes) || object.spec.template.spec.volumes.all(v, !has(v.hostPath))`,
				"the operator mounts no host path"),
			deny(`!(has(object.spec.template.spec.securityContext) && has(object.spec.template.spec.securityContext.runAsUser) && object.spec.template.spec.securityContext.runAsUser == 0) && `+
				`!(has(object.spec.template.spec.securityContext) && has(object.spec.template.spec.securityContext.seccompProfile) && object.spec.template.spec.securityContext.seccompProfile.type == "Unconfined")`,
				"the operator runs no pod as root nor unconfined by seccomp"),
			deny(eachContainer(`!(has(c.securityContext) && has(c.securityContext.privileged) && c.securityContext.privileged)`),
				"the operator runs no privileged container"),
			deny(eachContainer(`has(c.securityContext) && has(c.securityContext.allowPrivilegeEscalation) && c.securityContext.allowPrivilegeEscalation == false`),
				"the operator runs every container with allowPrivilegeEscalation false"),
			deny(eachContainer(`has(c.securityContext) && has(c.securityContext.capabilities) && has(c.securityContext.capabilities.drop) && `+
				`"ALL" in c.securityContext.capabilities.drop && !(has(c.securityContext.capabilities.add) && size(c.securityContext.capabilities.add) > 0)`),
				"the operator runs every container with all capabilities dropped and none added"),
			deny(eachContainer(`!(has(c.securityContext) && has(c.securityContext.runAsUser) && c.securityContext.runAsUser == 0) && `+
				`!(has(c.securityContext) && has(c.securityContext.seccompProfile) && c.securityContext.seccompProfile.type == "Unconfined")`),
				"the operator runs no container as root nor unconfined by seccomp"),
		}),
		policy(GuardBindingsName, append(rules("rbac.authorization.k8s.io", "clusterrolebindings"), rules("rbac.authorization.k8s.io", "rolebindings")...), operator, []admissionregistrationv1.Validation{
			deny(`request.resource.resource == "clusterrolebindings" ? object.metadata.name in `+list(slices.Sorted(mapKeys(clusterBindings(p)))...)+
				` : (object.metadata.name in `+list(slices.Sorted(mapKeys(roleBindings(p)))...)+` && request.namespace == `+quote(p.ArgoNamespace)+`)`,
				"the operator writes only the bindings it renders, by name"),
			deny(`object.roleRef.apiGroup == "rbac.authorization.k8s.io" && object.roleRef.kind == "ClusterRole" && object.roleRef.name == object.metadata.name`,
				"the operator binds each binding to the ClusterRole of its own name"),
			deny(`!has(object.subjects) || object.subjects.all(s, (s.kind + "/" + (has(s.namespace) ? s.namespace : "") + "/" + s.name) in `+
				subjectMap(p)+`[object.metadata.name])`,
				"the operator binds each binding to its rendered subjects only"),
		}),
	}
	purpose := map[string]string{
		GuardSecretsName:     "Bounds the operator's Secrets: none outside " + p.Namespace + " and " + p.ArgoNamespace + ", never a ServiceAccount token",
		GuardDeploymentsName: "Bounds the operator's Deployments: only in " + p.Namespace + ", as dotvirt's own accounts, official or admin-chosen images, unprivileged and off the host",
		GuardBindingsName:    "Bounds the operator's RBAC bindings: only the ones it renders, to their own roles and rendered subjects",
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
	return out
}

// imageAllowed is container c's image coming from an official repository (the
// repository then a tag or a digest, so a lookalike name fails) or being an
// image an admin chose.
func imageAllowed(p Params) string {
	var any []string
	for _, r := range Repositories {
		any = append(any, `c.image.startsWith(`+quote(r+"@sha256:")+`)`, `c.image.startsWith(`+quote(r+":")+`)`)
	}
	return "(" + strings.Join(any, " || ") + ` || c.image in ` + list(p.SpecImage, p.DotvirtImage, p.ForgejoImage) + ")"
}

// eachContainer holds rule (over c) for every container and init container.
func eachContainer(rule string) string {
	return `object.spec.template.spec.containers.all(c, ` + rule + `) && ` +
		`(!has(object.spec.template.spec.initContainers) || object.spec.template.spec.initContainers.all(c, ` + rule + `))`
}

// clusterBindings are the ClusterRoleBindings the operator renders, by name,
// with every subject any of its renders gives them.
func clusterBindings(p Params) map[string][]string {
	sa := func(ns, name string) string { return "ServiceAccount/" + ns + "/" + name }
	group := func(name string) string { return "Group//" + name }
	return map[string][]string{
		"dotvirt":                        {sa(p.Namespace, OperandAccounts[0])},
		"dotvirt-argocd-apply":           {sa(p.ArgoNamespace, p.ArgoController), sa(p.ArgoNamespace, PlatformSyncAccount)},
		"dotvirt-argocd-read":            {sa(p.ArgoNamespace, p.ArgoController)},
		PlatformSyncAccount:              {sa(p.ArgoNamespace, PlatformSyncAccount)},
		"dotvirt-platform-network-admin": {group("dotvirt-platform-admins")},
		"dotvirt-storage-catalog":        {group("system:authenticated")},
	}
}

// roleBindings are the RoleBindings the operator renders, in the ArgoCD
// namespace.
func roleBindings(p Params) map[string][]string {
	return map[string][]string{"dotvirt-sync-request": {"ServiceAccount/" + p.Namespace + "/" + OperandAccounts[0]}}
}

func subjectMap(p Params) string {
	all := clusterBindings(p)
	for k, v := range roleBindings(p) {
		all[k] = v
	}
	var entries []string
	for _, k := range slices.Sorted(mapKeys(all)) {
		entries = append(entries, quote(k)+": "+list(all[k]...))
	}
	return "{" + strings.Join(entries, ", ") + "}"
}

func mapKeys(m map[string][]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func rules(group, resource string) []admissionregistrationv1.NamedRuleWithOperations {
	return []admissionregistrationv1.NamedRuleWithOperations{{
		RuleWithOperations: admissionregistrationv1.RuleWithOperations{
			Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
			Rule:       admissionregistrationv1.Rule{APIGroups: []string{group}, APIVersions: []string{"v1"}, Resources: []string{resource}},
		},
	}}
}

func policy(name string, match []admissionregistrationv1.NamedRuleWithOperations, operator string, validations []admissionregistrationv1.Validation) *admissionregistrationv1.ValidatingAdmissionPolicy {
	return &admissionregistrationv1.ValidatingAdmissionPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: guardLabels()},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicySpec{
			FailurePolicy:    ptr.To(admissionregistrationv1.Fail),
			MatchConstraints: &admissionregistrationv1.MatchResources{ResourceRules: match},
			MatchConditions:  []admissionregistrationv1.MatchCondition{{Name: "operator", Expression: operator}},
			Validations:      validations,
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

// list is a CEL list of string literals.
func list(items ...string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
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

func diffPolicy(want, live *unstructured.Unstructured) string {
	var w, h admissionregistrationv1.ValidatingAdmissionPolicy
	if runtime.DefaultUnstructuredConverter.FromUnstructured(want.Object, &w) != nil ||
		runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, &h) != nil {
		return "cannot be read as a ValidatingAdmissionPolicy"
	}
	switch {
	case h.Spec.FailurePolicy == nil || *h.Spec.FailurePolicy != *w.Spec.FailurePolicy:
		return "does not fail closed"
	case h.Spec.ParamKind != nil || !slices.EqualFunc(h.Spec.Variables, w.Spec.Variables, func(a, b admissionregistrationv1.Variable) bool { return a == b }):
		return "takes parameters or variables the render does not"
	case h.Spec.MatchConstraints == nil || !sameMatch(h.Spec.MatchConstraints, w.Spec.MatchConstraints):
		return "matches other requests than the render"
	case !slices.EqualFunc(h.Spec.MatchConditions, w.Spec.MatchConditions, func(a, b admissionregistrationv1.MatchCondition) bool {
		return a.Name == b.Name && a.Expression == b.Expression
	}):
		return "applies to another account than " + strings.TrimPrefix(w.Spec.MatchConditions[0].Expression, "request.userInfo.username == ")
	}
	have := map[string]bool{}
	for _, v := range h.Spec.Validations {
		have[v.Expression] = true
	}
	for _, v := range w.Spec.Validations {
		if !have[v.Expression] {
			return fmt.Sprintf("lacks the rule %q", strings.TrimSuffix(v.Message, " (dotvirt operator guard)"))
		}
	}
	if len(h.Spec.Validations) != len(w.Spec.Validations) {
		return "holds rules the render does not"
	}
	return ""
}

func diffBinding(want, live *unstructured.Unstructured) string {
	var w, h admissionregistrationv1.ValidatingAdmissionPolicyBinding
	if runtime.DefaultUnstructuredConverter.FromUnstructured(want.Object, &w) != nil ||
		runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, &h) != nil {
		return "cannot be read as a ValidatingAdmissionPolicyBinding"
	}
	switch {
	case h.Spec.PolicyName != w.Spec.PolicyName:
		return "enforces another policy"
	case !slices.Equal(h.Spec.ValidationActions, w.Spec.ValidationActions):
		return "does not only deny"
	case h.Spec.ParamRef != nil || (h.Spec.MatchResources != nil && (len(h.Spec.MatchResources.ResourceRules) > 0 ||
		len(h.Spec.MatchResources.ExcludeResourceRules) > 0 || !selectsAll(h.Spec.MatchResources))):
		return "is narrowed to fewer requests"
	}
	return ""
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
