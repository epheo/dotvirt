package adminobjects

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

var params = GuardParams{Operator: "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller", Namespace: "dotvirt", ArgoNamespace: "openshift-gitops"}

func policyOf(t *testing.T, o Object) admissionregistrationv1.ValidatingAdmissionPolicy {
	t.Helper()
	var p admissionregistrationv1.ValidatingAdmissionPolicy
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object.Object, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func exprs(p admissionregistrationv1.ValidatingAdmissionPolicy) []string {
	var out []string
	for _, v := range p.Spec.Validations {
		out = append(out, v.Expression)
	}
	return out
}

func holds(t *testing.T, where string, got []string, needle string) {
	t.Helper()
	if !slices.ContainsFunc(got, func(e string) bool { return strings.Contains(e, needle) }) {
		t.Errorf("%s: no rule holds %s; got %q", where, needle, got)
	}
}

// The guard pins the operator's exact username and the install's
// namespaces, and matches only the two kinds it bounds, created or updated.
func TestGuardPinsTheOperatorAndItsNamespaces(t *testing.T) {
	objs := Guard(params)
	if len(objs) != 4 {
		t.Fatalf("guard = %d objects, want two policies and two bindings", len(objs))
	}
	for i, want := range []struct{ name, group, resource string }{{GuardSecretsName, "", "secrets"}, {GuardDeploymentsName, "apps", "deployments"}} {
		p := policyOf(t, objs[i])
		if p.Name != want.name || *p.Spec.FailurePolicy != admissionregistrationv1.Fail || objs[i].Purpose == "" {
			t.Errorf("policy %d = %s %q", i, p.Name, objs[i].Purpose)
		}
		rules := p.Spec.MatchConstraints.ResourceRules
		if len(rules) != 1 || !slices.Equal(rules[0].APIGroups, []string{want.group}) || !slices.Equal(rules[0].Resources, []string{want.resource}) ||
			!slices.Equal(rules[0].Operations, []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}) {
			t.Errorf("%s match = %+v", p.Name, rules)
		}
		if len(p.Spec.MatchConditions) != 1 || p.Spec.MatchConditions[0].Expression != `request.userInfo.username == "`+params.Operator+`"` {
			t.Errorf("%s condition = %+v, want the exact username", p.Name, p.Spec.MatchConditions)
		}
		var b admissionregistrationv1.ValidatingAdmissionPolicyBinding
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(objs[2+i].Object.Object, &b); err != nil {
			t.Fatal(err)
		}
		if b.Name != want.name || b.Spec.PolicyName != want.name || b.Spec.MatchResources != nil ||
			!slices.Equal(b.Spec.ValidationActions, []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}) {
			t.Errorf("binding %s = %+v", b.Name, b.Spec)
		}
	}
	sec, dep := exprs(policyOf(t, objs[0])), exprs(policyOf(t, objs[1]))
	holds(t, "secrets", sec, `object.type != "kubernetes.io/service-account-token"`)
	holds(t, "secrets", sec, `"kubernetes.io/service-account.name" in object.metadata.annotations`)
	holds(t, "secrets", sec, `request.namespace in ["dotvirt", "openshift-gitops"]`)
	holds(t, "deployments", dep, `request.namespace == "dotvirt"`)
	holds(t, "deployments", dep, `serviceAccountName in ["dotvirt", "dotvirt-forgejo"]`)
	for _, f := range []string{"hostNetwork", "hostPID", "hostIPC", "!has(v.hostPath)", "containers.all(c, " + unprivileged, "initContainers.all(c, " + unprivileged} {
		holds(t, "deployments", dep, f)
	}
	for _, f := range []string{"c.securityContext.privileged", "c.securityContext.allowPrivilegeEscalation"} {
		if !strings.Contains(unprivileged, f) {
			t.Errorf("container rule misses %s", f)
		}
	}
	for _, o := range objs {
		if _, ok := Allow(Allowed(params.Namespace), o.Object); !ok {
			t.Errorf("%s %s is rendered but not allowed", o.Object.GetKind(), o.Object.GetName())
		}
	}
}

func TestAllowRefusesAnythingElse(t *testing.T) {
	allowed := Allowed("dotvirt")
	ok := Guard(params)[0].Object
	for name, edit := range map[string]func(*unstructured.Unstructured){
		"another name":    func(o *unstructured.Unstructured) { o.SetName("cluster-admin") },
		"another kind":    func(o *unstructured.Unstructured) { o.SetKind("ClusterRoleBinding") },
		"another group":   func(o *unstructured.Unstructured) { o.SetAPIVersion("rbac.authorization.k8s.io/v1") },
		"another version": func(o *unstructured.Unstructured) { o.SetAPIVersion("admissionregistration.k8s.io/v1beta1") },
		"a namespace":     func(o *unstructured.Unstructured) { o.SetNamespace("kube-system") },
	} {
		o := ok.DeepCopy()
		edit(o)
		if _, allowed := Allow(allowed, o); allowed {
			t.Errorf("%s: allowed", name)
		}
	}
	if _, allowed := Allow(allowed, ok); !allowed {
		t.Error("the guard's own policy is refused")
	}
}

func TestManifestIsOneList(t *testing.T) {
	b, err := Manifest(Guard(params))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		APIVersion string
		Kind       string
		Items      []map[string]any
	}
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	if list.APIVersion != "v1" || list.Kind != "List" || len(list.Items) != 4 {
		t.Fatalf("manifest = %s %s with %d items", list.APIVersion, list.Kind, len(list.Items))
	}
	for i, name := range []string{GuardSecretsName, GuardDeploymentsName, GuardSecretsName, GuardDeploymentsName} {
		if md, _ := list.Items[i]["metadata"].(map[string]any); md["name"] != name {
			t.Errorf("item %d = %v, want %s", i, md, name)
		}
	}
	if strings.Contains(ApplyCommand, "force") || !strings.HasSuffix(ApplyCommand, "-f") {
		t.Errorf("ApplyCommand = %q: a fixed command ending in -f, never forcing", ApplyCommand)
	}
}

func TestCompare(t *testing.T) {
	want := Guard(params)
	moved := params
	moved.ArgoNamespace = "argocd"
	for name, c := range map[string]struct {
		live  func() *unstructured.Unstructured
		which int
		state State
	}{
		"as rendered":     {func() *unstructured.Unstructured { return want[0].Object.DeepCopy() }, 0, Installed},
		"missing":         {func() *unstructured.Unstructured { return nil }, 3, NotInstalled},
		"another install": {func() *unstructured.Unstructured { return Guard(moved)[0].Object }, 0, Outdated},
		"a validation dropped": {func() *unstructured.Unstructured {
			o := want[1].Object.DeepCopy()
			v, _, _ := unstructured.NestedSlice(o.Object, "spec", "validations")
			_ = unstructured.SetNestedSlice(o.Object, v[1:], "spec", "validations")
			return o
		}, 1, Outdated},
		"the condition widened": {func() *unstructured.Unstructured {
			o := want[0].Object.DeepCopy()
			_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"name": "operator", "expression": "true"}}, "spec", "matchConditions")
			return o
		}, 0, Outdated},
		"a warn-only binding": {func() *unstructured.Unstructured {
			o := want[2].Object.DeepCopy()
			_ = unstructured.SetNestedStringSlice(o.Object, []string{"Warn"}, "spec", "validationActions")
			return o
		}, 2, Outdated},
	} {
		if got, detail := Compare(want[c.which].Object, c.live()); got != c.state {
			t.Errorf("%s: %s (%s), want %s", name, got, detail, c.state)
		}
	}
}

func TestUsernameFromToken(t *testing.T) {
	enc := func(payload string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".s"
	}
	if got, err := UsernameFromToken(enc(`{"sub":"system:serviceaccount:ops:op"}`) + "\n"); err != nil || got != "system:serviceaccount:ops:op" {
		t.Errorf("got %q %v", got, err)
	}
	for _, bad := range []string{"", "a.b", enc(`{"sub":"alice"}`), enc(`{"sub":"system:serviceaccount:ops"}`), enc(`not json`)} {
		if got, err := UsernameFromToken(bad); err == nil {
			t.Errorf("%q: got %q, want an error", bad, got)
		}
	}
}
